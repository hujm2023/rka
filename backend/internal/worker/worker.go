// Package worker executes AI teaching tasks outside the HTTP request path. It
// leases tasks from PostgreSQL, heartbeats while a turn runs, stops quickly on
// user cancellation, and verifies unknown upstream state before any retry.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rka/internal/ai"
	"rka/internal/config"
	"rka/internal/teaching"
)

// Worker runs background teaching tasks outside the HTTP request path.
type Worker struct {
	cfg *config.Config
	log *slog.Logger
	ai  *ai.Service
}

func New(cfg *config.Config, log *slog.Logger, aiService *ai.Service) *Worker {
	return &Worker{cfg: cfg, log: log, ai: aiService}
}

// Run processes tasks until the context is cancelled. At most
// Worker.MaxConcurrency tasks execute at once; each uses its own lease.
func (w *Worker) Run(ctx context.Context) error {
	if err := recordHeartbeat(w.cfg); err != nil {
		return err
	}
	defer os.Remove(heartbeatPath(w.cfg))

	maxConcurrency := w.cfg.Worker.MaxConcurrency
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	poll := w.cfg.Worker.PollInterval
	if poll <= 0 {
		poll = time.Second
	}
	w.log.Info("worker started", "worker_id", w.cfg.Worker.ID, "max_concurrency", maxConcurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	if service := w.ai.Teaching(); service != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
				if _, err := service.ProcessKnowledgeIndex(ctx); err != nil && !errors.Is(err, context.Canceled) {
					w.log.Warn("knowledge index step failed", "error", err)
				}
			}
		}()
	}
	sem := make(chan struct{}, maxConcurrency)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		if _, err := w.ai.ReapExpired(ctx); err != nil {
			if !errors.Is(err, context.Canceled) {
				w.log.Warn("reap expired leases failed", "error", err)
			}
		} else if err := recordHeartbeat(w.cfg); err != nil {
			w.log.Warn("worker heartbeat failed", "error", err)
		}
	dispatch:
		for {
			select {
			case sem <- struct{}{}:
			default:
				break dispatch
			}
			task, err := w.ai.ClaimNext(ctx, w.cfg.Worker.ID, w.cfg.Worker.LeaseTTL)
			if err != nil {
				<-sem
				if !errors.Is(err, context.Canceled) {
					w.log.Warn("claim task failed", "error", err)
				}
				break
			}
			if task == nil {
				<-sem
				break
			}
			wg.Add(1)
			go func(task *ai.ClaimedTask) {
				defer wg.Done()
				defer func() { <-sem }()
				w.process(ctx, task)
			}(task)
		}
	}
}

// ProcessOne reaps and executes at most one task synchronously. It is the
// deterministic entry point used by integration tests and maintenance commands.
func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	if _, err := w.ai.ReapExpired(ctx); err != nil {
		return false, err
	}
	task, err := w.ai.ClaimNext(ctx, w.cfg.Worker.ID, w.cfg.Worker.LeaseTTL)
	if err != nil {
		return false, err
	}
	if task == nil {
		return false, nil
	}
	w.process(ctx, task)
	return true, nil
}

// process owns one claimed task from start to terminal state.
func (w *Worker) process(ctx context.Context, task *ai.ClaimedTask) {
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	var leaseLost atomic.Bool
	go w.heartbeatLoop(heartbeatCtx, task.ID, &leaseLost)

	// Bookkeeping must survive a slow upstream turn. A short per-operation
	// deadline used to expire in the middle of a long model call, which left the
	// task running while every event/terminal write failed. The bound only
	// protects the worker from a wedged database; a healthy turn finishes well
	// inside it.
	bookCtx, cancelBook := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancelBook()

	if err := w.execute(ctx, bookCtx, task, &leaseLost); err != nil && !errors.Is(err, ai.ErrLeaseLost) {
		w.log.Warn("task execution ended with error", "task_id", task.ID, "error", err)
	}
}

func (w *Worker) heartbeatLoop(ctx context.Context, taskID string, leaseLost *atomic.Bool) {
	interval := w.cfg.Worker.Heartbeat
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.ai.Heartbeat(ctx, taskID, w.cfg.Worker.ID, w.cfg.Worker.LeaseTTL); err != nil {
				if errors.Is(err, ai.ErrLeaseLost) {
					leaseLost.Store(true)
				}
				return
			}
		}
	}
}

func (w *Worker) execute(ctx, bookCtx context.Context, task *ai.ClaimedTask, leaseLost *atomic.Bool) error {
	teachingService := w.ai.Teaching()
	if teachingService == nil {
		return w.ai.FailTask(bookCtx, task.ID, w.cfg.Worker.ID, "ai_unavailable", "未配置 AI 教学执行服务。")
	}
	identity, err := teachingService.Identity(ctx, task.UserID)
	if err != nil {
		return w.ai.FailTask(bookCtx, task.ID, w.cfg.Worker.ID, "identity_error", "无法建立教学身份："+err.Error())
	}
	if task.Recovering {
		return w.recover(ctx, bookCtx, task, identity)
	}

	// Cancel may arrive between claiming and starting the upstream turn.
	if cancelRequested, status, err := w.ai.CancelRequested(ctx, task.ID); err == nil &&
		(cancelRequested || status == ai.StatusCancelling) {
		if task.TeachingTurn != "" {
			_ = teachingService.Cancel(bookCtx, teaching.TurnRef{
				Identity: identity, TaskID: task.ID, TurnID: task.TeachingTurn, SessionID: task.TeachingSession, AfterSeq: task.UpstreamSeq,
			})
		}
		return w.ai.FinishCancel(bookCtx, task.ID, w.cfg.Worker.ID, "用户取消了任务。")
	}

	history, err := w.ai.Conversation(ctx, task.ID)
	if err != nil {
		return w.ai.FailTask(bookCtx, task.ID, w.cfg.Worker.ID, "context_error", "读取对话记录失败："+err.Error())
	}
	prompt, history := splitLastUser(history, task.Context)
	request, err := teachingService.PrepareTurn(ctx, teaching.PrepareInput{
		UserID:  task.UserID,
		TaskID:  task.ID,
		Kind:    task.Kind,
		Context: task.Context,
		History: history,
		Prompt:  prompt,
		Hint:    boolContext(task.Context, "hint"),
	})
	if err != nil {
		return w.ai.FailTask(bookCtx, task.ID, w.cfg.Worker.ID, "context_error", "准备教学上下文失败："+err.Error())
	}
	request.Identity = identity

	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()
	var cancelRequested atomic.Bool
	go w.watchCancel(streamCtx, task.ID, &cancelRequested, cancelStream)

	var resume *teaching.TurnRef
	if task.ResumeTurn && task.TeachingTurn != "" {
		resume = &teaching.TurnRef{
			Identity: identity, TaskID: task.ID,
			TurnID: task.TeachingTurn, SessionID: task.TeachingSession, AfterSeq: task.UpstreamSeq,
		}
		request.ResumeReply = prompt
	}

	var sawQuestion bool
	emit := func(event teaching.Event) error {
		if leaseLost.Load() {
			return ai.ErrLeaseLost
		}
		if cancelRequested.Load() {
			return teaching.ErrCancelled
		}
		switch event.Type {
		case teachingEventDone, teachingEventError, teachingEventCancelled:
			// Terminal events are written by the worker after the turn outcome
			// is known; forwarding the upstream copy would duplicate them.
			return nil
		case teachingEventQuestion:
			sawQuestion = true
		}
		_, err := w.ai.AppendEvent(bookCtx, task.ID, event.Type, event.Payload)
		return err
	}

	request.SubmissionID = fmt.Sprintf("%s:%d", task.ID, task.Invocation)
	request.OnStarted = func(ref teaching.TurnRef) error {
		return w.ai.SetTeachingTurn(bookCtx, task.ID, w.cfg.Worker.ID, ref.TurnID, ref.SessionID)
	}
	request.CommitFrame = func(seq int64, events []teaching.Event) error {
		if leaseLost.Load() {
			return ai.ErrLeaseLost
		}
		if cancelRequested.Load() {
			return teaching.ErrCancelled
		}
		for _, event := range events {
			if event.Type == teachingEventQuestion {
				sawQuestion = true
			}
		}
		return w.ai.AppendFrame(bookCtx, task.ID, w.cfg.Worker.ID, seq, events)
	}
	if err := w.ai.StartIntent(bookCtx, task.ID, w.cfg.Worker.ID); err != nil {
		return err
	}
	ref, result, runErr := teachingService.RunTurn(streamCtx, request, resume, emit)
	if runErr != nil {
		return w.handleRunError(bookCtx, task, identity, ref, runErr, cancelRequested.Load(), leaseLost.Load())
	}
	if leaseLost.Load() {
		return ai.ErrLeaseLost
	}
	if cancelRequested.Load() {
		_ = teachingService.Cancel(bookCtx, ref)
		return w.ai.FinishCancel(bookCtx, task.ID, w.cfg.Worker.ID, "用户取消了任务。")
	}
	if result.Waiting {
		if !sawQuestion {
			if _, err := w.ai.AppendEvent(bookCtx, task.ID, ai.EventQuestion, map[string]any{"text": result.FinalText}); err != nil {
				return err
			}
		}
		return w.ai.WaitForUser(bookCtx, task.ID, w.cfg.Worker.ID, result)
	}
	if strings.TrimSpace(result.FinalText) == "" {
		return w.ai.FailTask(bookCtx, task.ID, w.cfg.Worker.ID, "empty_response", "AI 未返回文本。")
	}
	if _, err := w.ai.CompleteTask(bookCtx, task.ID, w.cfg.Worker.ID, result); err != nil {
		return err
	}
	w.log.Info("ai task completed", "task_id", task.ID, "kind", task.Kind, "attempt", task.Attempt)
	return nil
}

// recover verifies an unknown upstream turn. It never re-runs a turn whose
// upstream state cannot be confirmed.
func (w *Worker) recover(ctx, bookCtx context.Context, task *ai.ClaimedTask, identity teaching.Identity) error {
	if !task.UpstreamStarted && task.ResumeTurn && task.TeachingTurn != "" {
		return w.ai.RequeueUnstartedReply(bookCtx, task.ID, w.cfg.Worker.ID)
	}
	teachingService := w.ai.Teaching()
	if task.UpstreamWaiting {
		result, err := w.ai.WaitingResult(bookCtx, task.ID)
		if err != nil {
			return err
		}
		result.UpstreamSeq = task.UpstreamSeq
		return w.ai.WaitForUser(bookCtx, task.ID, w.cfg.Worker.ID, result)
	}
	if task.TeachingTurn == "" {
		if task.UpstreamStarted {
			if task.Attempt >= w.maxAttempts() {
				return w.ai.FailTask(bookCtx, task.ID, w.cfg.Worker.ID, "upstream_unresolved", "上游是否接受请求无法确认，已停止自动重试。")
			}
			return w.ai.MarkRecovering(bookCtx, task.ID, w.cfg.Worker.ID, w.retryBackoff(), "upstream_unknown", "创建回合的确认丢失，不自动重复调用。")
		}
		return w.ai.RequeueTask(bookCtx, task.ID, w.cfg.Worker.ID, w.retryBackoff(), "upstream_not_started", "回合尚未创建，已重新排队。")
	}
	recovery, err := teachingService.Recover(ctx, teaching.TurnRef{
		Identity: identity, TaskID: task.ID,
		TurnID: task.TeachingTurn, SessionID: task.TeachingSession, AfterSeq: task.UpstreamSeq,
	})
	if err != nil {
		return w.ai.MarkRecovering(bookCtx, task.ID, w.cfg.Worker.ID, w.retryBackoff(), "upstream_unknown", "核对上游状态失败："+err.Error())
	}
	switch recovery.State {
	case teaching.RecoveryCompleted:
		result := teaching.Result{TurnID: task.TeachingTurn}
		if recovery.Result != nil {
			result = *recovery.Result
		}
		if strings.TrimSpace(result.FinalText) != "" {
			if _, err := w.ai.AppendEvent(bookCtx, task.ID, ai.EventDelta, map[string]any{
				"text": result.FinalText, "recovered": true,
			}); err != nil {
				return err
			}
		}
		_, err := w.ai.CompleteTask(bookCtx, task.ID, w.cfg.Worker.ID, result)
		return err
	case teaching.RecoveryWaiting:
		if recovery.Result == nil {
			return w.ai.FailTask(bookCtx, task.ID, w.cfg.Worker.ID, "invalid_recovery", "上游未提供等待输入的内容。")
		}
		if _, err := w.ai.AppendEvent(bookCtx, task.ID, ai.EventQuestion, map[string]any{"text": recovery.Result.FinalText}); err != nil {
			return err
		}
		return w.ai.WaitForUser(bookCtx, task.ID, w.cfg.Worker.ID, *recovery.Result)
	case teaching.RecoveryRunning:
		return w.ai.MarkRecovering(bookCtx, task.ID, w.cfg.Worker.ID, w.retryBackoff(), "upstream_running", "上游回合仍在执行，稍后核对。")
	case teaching.RecoveryNotFound:
		return w.ai.RequeueTask(bookCtx, task.ID, w.cfg.Worker.ID, w.retryBackoff(), "upstream_not_found", "上游不存在该回合，已重新排队。")
	case teaching.RecoveryFailed:
		return w.ai.FailTask(bookCtx, task.ID, w.cfg.Worker.ID, "upstream_failed", recovery.Reason)
	default:
		if task.Attempt >= w.maxAttempts() {
			return w.ai.FailTask(bookCtx, task.ID, w.cfg.Worker.ID, "upstream_unresolved", "无法确认上游任务状态，已停止自动重试。")
		}
		return w.ai.MarkRecovering(bookCtx, task.ID, w.cfg.Worker.ID, w.retryBackoff(), "upstream_unknown", "无法确认上游任务状态，稍后再次核对。")
	}
}

func (w *Worker) handleRunError(bookCtx context.Context, task *ai.ClaimedTask, identity teaching.Identity, ref teaching.TurnRef, err error, cancelled, leaseLost bool) error {
	if leaseLost {
		return ai.ErrLeaseLost
	}
	if cancelled {
		_ = w.ai.Teaching().Cancel(bookCtx, ref)
		return w.ai.FinishCancel(bookCtx, task.ID, w.cfg.Worker.ID, "用户取消了任务。")
	}
	if teaching.IsCancelled(err) {
		// The local stream was cancelled but the user did not request it: the
		// worker is shutting down. The upstream may still be running, so the
		// task must be verified later instead of being marked cancelled.
		return w.ai.MarkRecovering(bookCtx, task.ID, w.cfg.Worker.ID, w.retryBackoff(), "worker_shutdown", "执行中断，正在核对上游任务状态。")
	}
	code, message := upstreamCode(err)
	if teaching.IsUnknownState(err) {
		if task.Attempt >= w.maxAttempts() {
			return w.ai.FailTask(bookCtx, task.ID, w.cfg.Worker.ID, "upstream_unresolved", "无法确认上游任务状态，已停止自动重试："+message)
		}
		return w.ai.MarkRecovering(bookCtx, task.ID, w.cfg.Worker.ID, w.retryBackoff(), code, message)
	}
	if teaching.IsRetryable(err) {
		if task.Attempt >= w.maxAttempts() {
			return w.ai.FailTask(bookCtx, task.ID, w.cfg.Worker.ID, code, message)
		}
		return w.ai.RequeueTask(bookCtx, task.ID, w.cfg.Worker.ID, w.retryBackoff(), code, message)
	}
	return w.ai.FailTask(bookCtx, task.ID, w.cfg.Worker.ID, code, message)
}

// ---------------------------------------------------------------- helpers

const (
	teachingEventDone      = "done"
	teachingEventError     = "error"
	teachingEventCancelled = "cancelled"
	teachingEventQuestion  = "question"
)

func (w *Worker) watchCancel(ctx context.Context, taskID string, requested *atomic.Bool, cancelStream context.CancelFunc) {
	interval := w.cfg.Worker.PollInterval
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	if interval > time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cancel, status, err := w.ai.CancelRequested(ctx, taskID)
			if err != nil {
				continue
			}
			if cancel || status == ai.StatusCancelling {
				requested.Store(true)
				cancelStream()
				return
			}
		}
	}
}

func (w *Worker) maxAttempts() int {
	if w.cfg.Worker.MaxAttempts > 0 {
		return w.cfg.Worker.MaxAttempts
	}
	return 3
}

func (w *Worker) retryBackoff() time.Duration {
	backoff := w.cfg.Worker.PollInterval * 10
	if backoff < 100*time.Millisecond {
		backoff = 100 * time.Millisecond
	}
	if backoff > 30*time.Second {
		backoff = 30 * time.Second
	}
	return backoff
}

func upstreamCode(err error) (string, string) {
	var upstream *teaching.UpstreamError
	if errors.As(err, &upstream) {
		code := upstream.Code
		if code == "" {
			code = "upstream_error"
		}
		message := strings.TrimSpace(upstream.Message)
		if message == "" {
			message = err.Error()
		}
		return code, message
	}
	if errors.Is(err, teaching.ErrUnavailable) {
		return "ai_unavailable", "教学执行后端未配置。"
	}
	return "worker_error", err.Error()
}

// splitLastUser separates the current user message from prior conversation
// history. The initial task prompt lives in the task context instead.
func splitLastUser(history []teaching.Message, taskContext map[string]any) (string, []teaching.Message) {
	if len(history) > 0 {
		last := history[len(history)-1]
		if last.Role == "user" {
			return last.Content, history[:len(history)-1]
		}
	}
	prompt, _ := taskContext["prompt"].(string)
	return strings.TrimSpace(prompt), history
}

func boolContext(values map[string]any, key string) bool {
	if values == nil {
		return false
	}
	value, _ := values[key].(bool)
	return value
}

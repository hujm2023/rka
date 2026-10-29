package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"rka/internal/httpx"
	"rka/internal/store"
	"rka/internal/teaching"
)

// Task states. The legal transitions are enforced in one place (this file) so
// HTTP handlers, worker and tests cannot invent different state machines.
const (
	StatusQueued      = "queued"
	StatusRunning     = "running"
	StatusWaitingUser = "waiting_user"
	StatusCompleted   = "completed"
	StatusFailed      = "failed"
	StatusCancelling  = "cancelling"
	StatusCancelled   = "cancelled"
	StatusRecovering  = "recovering"
)

// Event types persisted in ai_events. Anything else is rejected so the browser
// stream stays a closed protocol.
const (
	EventAccepted  = "accepted"
	EventProgress  = "progress"
	EventDelta     = "delta"
	EventCitation  = "citation"
	EventQuestion  = "question"
	EventDone      = "done"
	EventError     = "error"
	EventCancelled = "cancelled"
)

var allowedKinds = map[string]bool{
	"hint": true, "explain": true, "followup": true,
	"review_case": true, "review_essay": true, "path": true, "report": true,
}

var allowedEventTypes = map[string]bool{
	EventProgress: true,
	EventAccepted: true, EventDelta: true, EventCitation: true, EventQuestion: true,
	EventDone: true, EventError: true, EventCancelled: true,
}

// Event is one persisted, resumable task event.
type Event struct {
	Seq       int            `json:"seq"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt time.Time      `json:"createdAt"`
}

// ClaimedTask is the worker-facing task projection. It carries the upstream
// turn coordinates that are never exposed to browsers.
type ClaimedTask struct {
	UpstreamStarted bool
	UpstreamSeq     int64
	UpstreamWaiting bool
	Invocation      int
	Task
	UserID          string
	Recovering      bool
	Attempt         int
	TeachingUserID  string
	TeachingSession string
	TeachingTurn    string
	CancelRequested bool
	PreviousStatus  string
	LeaseOwner      string
	// ResumeTurn means the next turn continues the existing upstream turn
	// (the user replied to a waiting question) instead of starting a new one.
	ResumeTurn bool
}

// ErrLeaseLost means the worker no longer owns the task lease.
var ErrLeaseLost = errors.New("task lease lost")

const taskSelectColumns = `t.id::text, t.kind, t.status, t.context, t.attempt, t.cancel_requested,
	t.error_code, t.error_message, t.created_at, t.started_at, t.finished_at`

func scanTask(row pgx.Row) (Task, error) {
	var (
		task     Task
		context  []byte
		attempt  int
		cancel   bool
		started  *time.Time
		finished *time.Time
	)
	err := row.Scan(&task.ID, &task.Kind, &task.Status, &context, &attempt, &cancel,
		&task.ErrorCode, &task.ErrorText, &task.CreatedAt, &started, &finished)
	if err != nil {
		return Task{}, store.MapError(err)
	}
	task.Context = map[string]any{}
	_ = json.Unmarshal(context, &task.Context)
	task.Attempts = attempt
	task.CancelRequested = cancel
	task.StartedAt = started
	task.FinishedAt = finished
	return task, nil
}

// ---------------------------------------------------------------- create

// CreateTask is the contract used by other domain packages (writing, practice,
// admin). It is idempotent on (UserID, IdempotencyKey).
func (s *Service) CreateTask(ctx context.Context, in CreateTaskInput) (Task, error) {
	task, _, err := s.createTask(ctx, in)
	return task, err
}

func (s *Service) createTask(ctx context.Context, in CreateTaskInput) (Task, bool, error) {
	if strings.TrimSpace(in.UserID) == "" {
		return Task{}, false, httpx.NewError(http.StatusBadRequest, "bad_request", "创建 AI 任务需要登录用户。")
	}
	kind := strings.TrimSpace(in.Kind)
	if !allowedKinds[kind] {
		return Task{}, false, httpx.NewError(http.StatusBadRequest, "invalid_kind", "不支持的任务类型："+kind)
	}
	key := strings.TrimSpace(in.IdempotencyKey)
	if key == "" {
		return Task{}, false, httpx.NewError(http.StatusBadRequest, "idempotency_key_required", "请提供 idempotencyKey，以避免重复创建任务。")
	}
	if len(key) > 200 {
		return Task{}, false, httpx.NewError(http.StatusBadRequest, "bad_request", "idempotencyKey 过长。")
	}
	taskContext := in.Context
	if taskContext == nil {
		taskContext = map[string]any{}
	}
	if prompt, _ := taskContext["prompt"].(string); len(prompt) > 12000 {
		return Task{}, false, httpx.NewError(http.StatusBadRequest, "prompt_too_long", "单次提问最多 12000 个字符，请分段提问。")
	}

	// An idempotent replay must succeed even when the budget has just been
	// exhausted, so the lookup comes before all quota checks.
	if existing, err := s.taskByKey(ctx, in.UserID, key); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return Task{}, false, err
	}

	if reason := s.unavailableReason(ctx); reason != "" {
		return Task{}, false, httpx.NewError(http.StatusServiceUnavailable, "ai_unavailable", reason)
	}

	encoded, err := json.Marshal(taskContext)
	if err != nil {
		return Task{}, false, httpx.NewError(http.StatusBadRequest, "bad_request", "任务上下文不是有效 JSON。")
	}
	var taskID string
	var created bool
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := lockAdmission(ctx, tx); err != nil {
			return err
		}
		var existing string
		err := tx.QueryRow(ctx, `select id::text from ai_tasks where user_id=$1::uuid and idempotency_key=$2`, in.UserID, key).Scan(&existing)
		if err == nil {
			taskID = existing
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := s.checkAdmission(ctx, tx, ""); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
			insert into ai_tasks (user_id, kind, context, idempotency_key)
			values ($1::uuid, $2, $3::jsonb, $4)
			on conflict (user_id, idempotency_key) do nothing
			returning id::text`, in.UserID, kind, encoded, key).Scan(&taskID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // lost the race; the caller resolves the existing row below
		}
		if err != nil {
			if store.IsUniqueViolation(err) {
				return nil
			}
			return fmt.Errorf("create ai task: %w", err)
		}
		if _, err := tx.Exec(ctx, `insert into ai_invocations(task_id,invocation)values($1::uuid,1)`, taskID); err != nil {
			return err
		}
		created = true
		if prompt, _ := taskContext["prompt"].(string); strings.TrimSpace(prompt) != "" {
			if _, err := tx.Exec(ctx, `
				insert into ai_messages (task_id, role, content) values ($1::uuid, 'user', $2)`,
				taskID, prompt); err != nil {
				return fmt.Errorf("store initial ai message: %w", err)
			}
		}
		_, err = appendEventTx(ctx, tx, taskID, EventAccepted, map[string]any{"status": StatusQueued, "kind": kind})
		return err
	})
	if err != nil {
		return Task{}, false, err
	}
	if !created {
		existing, err := s.taskByKey(ctx, in.UserID, key)
		if err != nil {
			return Task{}, false, err
		}
		return existing, false, nil
	}
	task, err := s.TaskForUser(ctx, in.UserID, taskID)
	if err != nil {
		return Task{}, false, err
	}
	return task, true, nil
}

func (s *Service) taskByKey(ctx context.Context, userID, key string) (Task, error) {
	row := s.db.Pool.QueryRow(ctx, `
		select `+taskSelectColumns+`
		from ai_tasks t
		where t.user_id = $1::uuid and t.idempotency_key = $2 and t.deleted_at is null`, userID, key)
	return scanTask(row)
}

// TaskForUser returns one task owned by the user (or not found).
func (s *Service) TaskForUser(ctx context.Context, userID, taskID string) (Task, error) {
	row := s.db.Pool.QueryRow(ctx, `
		select `+taskSelectColumns+`
		from ai_tasks t
		where t.id = $1::uuid and t.user_id = $2::uuid and t.deleted_at is null`, taskID, userID)
	return scanTask(row)
}

// LoadTask loads a task without an ownership check (worker/admin use).
func (s *Service) LoadTask(ctx context.Context, taskID string) (Task, error) {
	row := s.db.Pool.QueryRow(ctx, `
		select `+taskSelectColumns+`
		from ai_tasks t
		where t.id = $1::uuid and t.deleted_at is null`, taskID)
	return scanTask(row)
}

// ---------------------------------------------------------------- events

// AppendEvent persists one event with a strictly monotonic per-task sequence.
func (s *Service) AppendEvent(ctx context.Context, taskID, eventType string, payload map[string]any) (int, error) {
	var seq int
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var err error
		seq, err = appendEventTx(ctx, tx, taskID, eventType, payload)
		return err
	})
	return seq, err
}

func appendEventTx(ctx context.Context, tx pgx.Tx, taskID, eventType string, payload map[string]any) (int, error) {
	if !allowedEventTypes[eventType] {
		return 0, fmt.Errorf("ai: unsupported event type %q", eventType)
	}
	// Serialise event writers on the task row. This makes max(seq)+1 safe and
	// keeps the unique(task_id, seq) guarantee under concurrent cancel/worker
	// activity.
	var locked string
	if err := tx.QueryRow(ctx, `select id::text from ai_tasks where id = $1::uuid for update`, taskID).Scan(&locked); err != nil {
		return 0, store.MapError(err)
	}
	var seq int
	if err := tx.QueryRow(ctx,
		`select coalesce(max(seq), 0) + 1 from ai_events where task_id = $1::uuid`, taskID).Scan(&seq); err != nil {
		return 0, fmt.Errorf("next ai event seq: %w", err)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("encode ai event: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		insert into ai_events (task_id, seq, type, payload)
		values ($1::uuid, $2, $3, $4::jsonb)`, taskID, seq, eventType, encoded); err != nil {
		return 0, fmt.Errorf("insert ai event: %w", err)
	}
	return seq, nil
}

// EventsAfter returns persisted events with seq greater than after.
func (s *Service) EventsAfter(ctx context.Context, taskID string, after, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Pool.Query(ctx, `
		select seq, type, payload, created_at
		from ai_events
		where task_id = $1::uuid and seq > $2
		order by seq
		limit $3`, taskID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]Event, 0, 8)
	for rows.Next() {
		var (
			event   Event
			payload []byte
		)
		if err := rows.Scan(&event.Seq, &event.Type, &payload, &event.CreatedAt); err != nil {
			return nil, err
		}
		event.Payload = map[string]any{}
		_ = json.Unmarshal(payload, &event.Payload)
		events = append(events, event)
	}
	return events, rows.Err()
}

// RecentEvents returns the newest events in ascending sequence order.
func (s *Service) RecentEvents(ctx context.Context, taskID string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Pool.Query(ctx, `
		select seq, type, payload, created_at from (
			select seq, type, payload, created_at from ai_events
			where task_id = $1::uuid order by seq desc limit $2
		) recent order by seq`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]Event, 0, limit)
	for rows.Next() {
		var (
			event   Event
			payload []byte
		)
		if err := rows.Scan(&event.Seq, &event.Type, &payload, &event.CreatedAt); err != nil {
			return nil, err
		}
		event.Payload = map[string]any{}
		_ = json.Unmarshal(payload, &event.Payload)
		events = append(events, event)
	}
	return events, rows.Err()
}

// ---------------------------------------------------------------- cancel / messages / delete

// RequestCancel marks a task for cancellation. Queued and waiting tasks cancel
// immediately; running tasks are stopped by the worker that owns the lease.
func (s *Service) RequestCancel(ctx context.Context, userID, taskID string) (Task, error) {
	var result Task
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var (
			status string
			owner  string
		)
		err := tx.QueryRow(ctx, `
			select status, lease_owner from ai_tasks
			where id = $1::uuid and user_id = $2::uuid and deleted_at is null
			for update`, taskID, userID).Scan(&status, &owner)
		if err != nil {
			return store.MapError(err)
		}
		switch status {
		case StatusCancelled:
			// Idempotent retry of cancel.
		case StatusCompleted, StatusFailed:
			return httpx.NewError(http.StatusConflict, "not_cancellable", "任务已结束，无法取消。")
		case StatusQueued, StatusWaitingUser, StatusRecovering:
			if _, err := tx.Exec(ctx, `
				update ai_tasks set status = 'cancelled', cancel_requested = true,
					finished_at = now(), lease_owner = '', lease_until = null,
					error_code = 'cancelled', error_message = '任务已取消。'
				where id = $1::uuid`, taskID); err != nil {
				return err
			}
			if _, err := appendEventTx(ctx, tx, taskID, EventCancelled, map[string]any{"reason": "用户取消了任务。"}); err != nil {
				return err
			}
		default: // running, cancelling
			if _, err := tx.Exec(ctx, `
				update ai_tasks set status = 'cancelling', cancel_requested = true
				where id = $1::uuid`, taskID); err != nil {
				return err
			}
		}
		result, err = s.loadTaskTx(ctx, tx, taskID)
		return err
	})
	return result, err
}

// PostMessage appends a user message and re-queues the task for another turn.
func (s *Service) PostMessage(ctx context.Context, userID, taskID, content string) (Task, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return Task{}, httpx.NewError(http.StatusBadRequest, "empty_message", "追问内容不能为空。")
	}
	if len(content) > 12000 {
		return Task{}, httpx.NewError(http.StatusBadRequest, "message_too_long", "单次追问最多 12000 个字符，请分段提问。")
	}
	var result Task
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := lockAdmission(ctx, tx); err != nil {
			return err
		}
		var status string
		err := tx.QueryRow(ctx, `
			select status from ai_tasks
			where id = $1::uuid and user_id = $2::uuid and deleted_at is null
			for update`, taskID, userID).Scan(&status)
		if err != nil {
			return store.MapError(err)
		}
		switch status {
		case StatusQueued, StatusRunning, StatusCancelling, StatusRecovering:
			return httpx.NewError(http.StatusConflict, "task_busy", "任务正在执行，请等待完成或先取消后再追问。")
		case StatusCancelled:
			return httpx.NewError(http.StatusConflict, "task_closed", "任务已取消，无法继续。")
		}
		if err := s.checkAdmission(ctx, tx, taskID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			insert into ai_messages (task_id, role, content) values ($1::uuid, 'user', $2)`, taskID, content); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			update ai_tasks set status = 'queued', cancel_requested = false, attempt = 0,
				resume_turn = (status = 'waiting_user'), invocation=greatest(invocation,coalesce((select max(i.invocation) from ai_invocations i where i.task_id=ai_tasks.id),0))+1,
                upstream_started=false,upstream_waiting=false,
                teaching_turn_id=case when status='waiting_user' then teaching_turn_id else '' end,
                teaching_session_id=case when status='waiting_user' then teaching_session_id else '' end,
                upstream_seq=case when status='waiting_user' then upstream_seq else 0 end,
				error_code = '', error_message = '', finished_at = null, lease_owner = '', lease_until = null
			where id = $1::uuid`, taskID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into ai_invocations(task_id,invocation)select id,invocation from ai_tasks where id=$1::uuid`, taskID); err != nil {
			return err
		}
		if _, err := appendEventTx(ctx, tx, taskID, EventAccepted, map[string]any{"status": StatusQueued, "reason": "message"}); err != nil {
			return err
		}
		result, err = s.loadTaskTx(ctx, tx, taskID)
		return err
	})
	return result, err
}

// DeleteTask soft-deletes a finished task so a wrong AI summary can be removed
// from the learner's history without destroying the audit trail.
func (s *Service) DeleteTask(ctx context.Context, userID, taskID string) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		var status string
		err := tx.QueryRow(ctx, `
			select status from ai_tasks
			where id = $1::uuid and user_id = $2::uuid and deleted_at is null
			for update`, taskID, userID).Scan(&status)
		if err != nil {
			return store.MapError(err)
		}
		switch status {
		case StatusCompleted, StatusFailed, StatusCancelled:
		default:
			return httpx.NewError(http.StatusConflict, "task_busy", "任务尚未结束，完成或取消后才能删除。")
		}
		_, err = tx.Exec(ctx, `
			update ai_tasks set deleted_at = now(), lease_owner = '', lease_until = null
			where id = $1::uuid`, taskID)
		return err
	})
}

func (s *Service) loadTaskTx(ctx context.Context, tx pgx.Tx, taskID string) (Task, error) {
	row := tx.QueryRow(ctx, `
		select `+taskSelectColumns+`
		from ai_tasks t where t.id = $1::uuid`, taskID)
	return scanTask(row)
}

// Conversation returns the persisted messages in order.
func (s *Service) Conversation(ctx context.Context, taskID string) ([]teaching.Message, error) {
	rows, err := s.db.Pool.Query(ctx, `
		select role, content from ai_messages
		where task_id = $1::uuid order by created_at, id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []teaching.Message
	for rows.Next() {
		var message teaching.Message
		if err := rows.Scan(&message.Role, &message.Content); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

// ---------------------------------------------------------------- worker lease protocol

// ClaimNext atomically leases the oldest runnable task. It returns nil when the
// queue is empty. A task previously marked recovering is returned with
// Recovering=true so the worker verifies the upstream before re-running it.
func (s *Service) ClaimNext(ctx context.Context, workerID string, leaseTTL time.Duration) (*ClaimedTask, error) {
	if leaseTTL <= 0 {
		leaseTTL = time.Minute
	}
	var claimed *ClaimedTask
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var (
			task            Task
			contextRaw      []byte
			attempt         int
			cancel          bool
			previous        string
			userID          string
			teachingUser    string
			teachingSess    string
			teachingTurn    string
			resumeTurn      bool
			upstreamStarted bool
			upstreamSeq     int64
			upstreamWaiting bool
			invocation      int
			started         *time.Time
			finished        *time.Time
		)
		err := tx.QueryRow(ctx, `
			select t.id::text, t.kind, t.status, t.context, t.attempt, t.cancel_requested,
			       t.error_code, t.error_message, t.created_at, t.started_at, t.finished_at,
			       t.user_id::text, t.teaching_user_id, t.teaching_session_id, t.teaching_turn_id,
			       t.resume_turn, t.upstream_started, t.upstream_seq, t.upstream_waiting, t.invocation
			from ai_tasks t
			where t.status in ('queued', 'recovering') and t.deleted_at is null
			  and (t.lease_until is null or t.lease_until < now())
			order by t.created_at
			limit 1
			for update skip locked`).
			Scan(&task.ID, &task.Kind, &task.Status, &contextRaw, &attempt, &cancel,
				&task.ErrorCode, &task.ErrorText, &task.CreatedAt, &started, &finished,
				&userID, &teachingUser, &teachingSess, &teachingTurn, &resumeTurn, &upstreamStarted, &upstreamSeq, &upstreamWaiting, &invocation)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claim ai task: %w", err)
		}
		previous = task.Status
		task.Context = map[string]any{}
		_ = json.Unmarshal(contextRaw, &task.Context)
		task.Attempts = attempt
		task.CancelRequested = cancel
		task.StartedAt = started
		task.FinishedAt = finished
		if _, err := tx.Exec(ctx, `
			update ai_tasks set status = 'running', lease_owner = $2,
				lease_until = now() + make_interval(secs => $3),
				heartbeat_at = now(), started_at = coalesce(started_at, now()),
				attempt = attempt + 1, error_code = '', error_message = ''
			where id = $1::uuid`, task.ID, workerID, int(leaseTTL.Seconds())); err != nil {
			return fmt.Errorf("lease ai task: %w", err)
		}
		task.Status = StatusRunning
		claimed = &ClaimedTask{
			Task:            task,
			UserID:          userID,
			Recovering:      previous == StatusRecovering,
			Attempt:         attempt + 1,
			TeachingUserID:  teachingUser,
			TeachingSession: teachingSess,
			TeachingTurn:    teachingTurn,
			CancelRequested: cancel,
			PreviousStatus:  previous,
			LeaseOwner:      workerID,
			ResumeTurn:      resumeTurn,
			UpstreamStarted: upstreamStarted, UpstreamSeq: upstreamSeq, UpstreamWaiting: upstreamWaiting, Invocation: invocation,
		}
		return nil
	})
	return claimed, err
}

// Heartbeat extends the lease. A lost lease returns ErrLeaseLost so the worker
// stops writing to a task that another worker may own now.
func (s *Service) Heartbeat(ctx context.Context, taskID, workerID string, leaseTTL time.Duration) error {
	if leaseTTL <= 0 {
		leaseTTL = time.Minute
	}
	tag, err := s.db.Pool.Exec(ctx, `
		update ai_tasks set lease_until = now() + make_interval(secs => $3), heartbeat_at = now()
		where id = $1::uuid and lease_owner = $2 and status in ('running', 'cancelling')`,
		taskID, workerID, int(leaseTTL.Seconds()))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// ReapExpired recovers tasks whose worker died. A running task is moved to
// recovering (never straight back to queued) because its upstream turn may have
// been accepted. A cancelling task with a dead lease becomes cancelled.
func (s *Service) ReapExpired(ctx context.Context) (int, error) {
	var reaped int
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			select id::text, status from ai_tasks
			where status in ('running', 'cancelling')
			  and lease_until is not null and lease_until < now()
			for update skip locked`)
		if err != nil {
			return err
		}
		type candidate struct{ id, status string }
		var candidates []candidate
		for rows.Next() {
			var item candidate
			if err := rows.Scan(&item.id, &item.status); err != nil {
				rows.Close()
				return err
			}
			candidates = append(candidates, item)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, item := range candidates {
			if item.status == StatusCancelling {
				if _, err := tx.Exec(ctx, `
					update ai_tasks set status = 'cancelled', finished_at = now(),
						lease_owner = '', lease_until = null,
						error_code = 'cancelled', error_message = '任务已取消。'
					where id = $1::uuid`, item.id); err != nil {
					return err
				}
				if _, err := appendEventTx(ctx, tx, item.id, EventCancelled, map[string]any{"reason": "用户取消了任务。"}); err != nil {
					return err
				}
			} else {
				if _, err := tx.Exec(ctx, `
					update ai_tasks set status = 'recovering', lease_owner = '', lease_until = now(),
						heartbeat_at = null, error_code = 'lease_expired',
						error_message = '执行超时，正在核对上游任务状态。'
					where id = $1::uuid`, item.id); err != nil {
					return err
				}
				if _, err := appendEventTx(ctx, tx, item.id, EventError, map[string]any{
					"code": "lease_expired", "message": "执行超时，正在核对上游任务状态。", "recovering": true,
				}); err != nil {
					return err
				}
			}
			reaped++
		}
		return nil
	})
	return reaped, err
}

// SetTeachingTurn records the upstream turn identity for recovery.
func (s *Service) SetTeachingTurn(ctx context.Context, taskID, workerID, turnID, sessionID string) error {
	tag, err := s.db.Pool.Exec(ctx, `
		update ai_tasks set teaching_turn_id = $3, teaching_session_id = $4,
        upstream_seq=case when teaching_turn_id=$3 then upstream_seq else 0 end,upstream_started=true,upstream_waiting=false
		where id = $1::uuid and lease_owner = $2`, taskID, workerID, turnID, sessionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLeaseLost
	}
	return nil
}

// CompleteTask stores the assistant message and the terminal done event.
func (s *Service) CompleteTask(ctx context.Context, taskID, workerID string, result teaching.Result) (string, error) {
	finalStatus := StatusCompleted
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		var (
			status string
			cancel bool
		)
		err := tx.QueryRow(ctx, `
			select status, cancel_requested from ai_tasks
			where id = $1::uuid and lease_owner = $2
			for update`, taskID, workerID).Scan(&status, &cancel)
		if err != nil {
			return store.MapError(err)
		}
		if cancel || status == StatusCancelling {
			finalStatus = StatusCancelled
			if _, err := tx.Exec(ctx, `
				update ai_tasks set status = 'cancelled', finished_at = now(),
					lease_owner = '', lease_until = null,
					error_code = 'cancelled', error_message = '任务已取消。'
				where id = $1::uuid`, taskID); err != nil {
				return err
			}
			_, err := appendEventTx(ctx, tx, taskID, EventCancelled, map[string]any{"reason": "用户取消了任务。"})
			return err
		}
		if _, err := tx.Exec(ctx, `
			update ai_tasks set status = 'completed', finished_at = now(), resume_turn = false,
				lease_owner = '', lease_until = null, error_code = '', error_message = ''
			where id = $1::uuid`, taskID); err != nil {
			return err
		}
		if strings.TrimSpace(result.FinalText) != "" {
			if _, err := tx.Exec(ctx, `
				insert into ai_messages (task_id, role, content) values ($1::uuid, 'assistant', $2)`,
				taskID, result.FinalText); err != nil {
				return err
			}
		}
		_, err = appendEventTx(ctx, tx, taskID, EventDone, map[string]any{"status": StatusCompleted})
		return err
	})
	return finalStatus, err
}

// WaitForUser parks a task until the next user message.
func (s *Service) WaitForUser(ctx context.Context, taskID, workerID string, result teaching.Result) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		var cancelled bool
		if err := tx.QueryRow(ctx, `select cancel_requested or status='cancelling' from ai_tasks where id=$1::uuid and lease_owner=$2 for update`, taskID, workerID).Scan(&cancelled); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrLeaseLost
			}
			return err
		}
		if cancelled {
			if _, err := tx.Exec(ctx, `update ai_tasks set status='cancelled',finished_at=now(),lease_owner='',lease_until=null,error_code='cancelled' where id=$1::uuid`, taskID); err != nil {
				return err
			}
			_, err := appendEventTx(ctx, tx, taskID, EventCancelled, map[string]any{"reason": "用户取消了任务。"})
			return err
		}
		tag, err := tx.Exec(ctx, `
			update ai_tasks set status = 'waiting_user', lease_owner = '', lease_until = null,
				resume_turn = false, heartbeat_at = null, error_code = '', error_message = '',
                upstream_seq=greatest(upstream_seq,$3),upstream_waiting=true
			where id = $1::uuid and lease_owner = $2`, taskID, workerID, result.UpstreamSeq)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrLeaseLost
		}
		if strings.TrimSpace(result.FinalText) != "" {
			if _, err := tx.Exec(ctx, `
				insert into ai_messages (task_id, role, content) values ($1::uuid, 'assistant', $2)`,
				taskID, result.FinalText); err != nil {
				return err
			}
		}
		return nil
	})
}

// FailTask writes the terminal error and releases the lease. If the user
// cancelled while the turn was failing, cancellation wins and the error is not
// written as a failure.
func (s *Service) FailTask(ctx context.Context, taskID, workerID, code, message string) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		var (
			status string
			cancel bool
		)
		err := tx.QueryRow(ctx, `
			select status, cancel_requested from ai_tasks
			where id = $1::uuid and lease_owner = $2
			for update`, taskID, workerID).Scan(&status, &cancel)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLeaseLost
		}
		if err != nil {
			return err
		}
		if cancel || status == StatusCancelling {
			if _, err := tx.Exec(ctx, `
				update ai_tasks set status = 'cancelled', finished_at = now(),
					lease_owner = '', lease_until = null,
					error_code = 'cancelled', error_message = '任务已取消。'
				where id = $1::uuid`, taskID); err != nil {
				return err
			}
			_, err := appendEventTx(ctx, tx, taskID, EventCancelled, map[string]any{"reason": "用户取消了任务。"})
			return err
		}
		if _, err := tx.Exec(ctx, `
			update ai_tasks set status = 'failed', finished_at = now(),
				lease_owner = '', lease_until = null, error_code = $2, error_message = $3
			where id = $1::uuid`, taskID, code, message); err != nil {
			return err
		}
		_, err = appendEventTx(ctx, tx, taskID, EventError, map[string]any{"code": code, "message": message})
		return err
	})
}

// RequeueTask returns a task to the queue after a retryable failure and delays
// the next claim by backoff.
func (s *Service) RequeueTask(ctx context.Context, taskID, workerID string, backoff time.Duration, code, message string) error {
	if backoff < 0 {
		backoff = 0
	}
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
		update ai_tasks set status = 'queued', lease_owner = '', upstream_started=false,
            teaching_turn_id='',teaching_session_id='',upstream_seq=0,upstream_waiting=false,resume_turn=false,
			lease_until = now() + make_interval(secs => $3),
			error_code = $4, error_message = $5
		where id = $1::uuid and lease_owner = $2`, taskID, workerID, int(backoff.Seconds()), code, message)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrLeaseLost
		}
		_, err = appendEventTx(ctx, tx, taskID, EventProgress, map[string]any{"message": "上游暂时中断，正在自动重试…", "retrying": true})
		return err
	})
}

// MarkRecovering parks a task until the upstream turn can be checked again.
func (s *Service) MarkRecovering(ctx context.Context, taskID, workerID string, backoff time.Duration, code, message string) error {
	if backoff < 0 {
		backoff = 0
	}
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			update ai_tasks set status = 'recovering', lease_owner = '',
				lease_until = now() + make_interval(secs => $3),
				heartbeat_at = null, error_code = $4, error_message = $5
			where id = $1::uuid and lease_owner = $2`, taskID, workerID, int(backoff.Seconds()), code, message)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrLeaseLost
		}
		_, err = appendEventTx(ctx, tx, taskID, EventError, map[string]any{
			"code": code, "message": message, "recovering": true,
		})
		return err
	})
}

// CancelRequested reports whether the user asked to stop the task.
func (s *Service) CancelRequested(ctx context.Context, taskID string) (bool, string, error) {
	var (
		cancel bool
		status string
	)
	err := s.db.Pool.QueryRow(ctx,
		`select cancel_requested, status from ai_tasks where id = $1::uuid`, taskID).Scan(&cancel, &status)
	if err != nil {
		return false, "", store.MapError(err)
	}
	return cancel, status, nil
}

// FinishCancel ends a task after the worker stopped the upstream turn.
func (s *Service) FinishCancel(ctx context.Context, taskID, workerID, reason string) error {
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			update ai_tasks set status = 'cancelled', finished_at = now(),
				lease_owner = '', lease_until = null, cancel_requested = true,
				error_code = 'cancelled', error_message = '任务已取消。'
			where id = $1::uuid`, taskID); err != nil {
			return err
		}
		_, err := appendEventTx(ctx, tx, taskID, EventCancelled, map[string]any{"reason": reason})
		return err
	})
}

// ---------------------------------------------------------------- listing / metrics

// ListTasks returns tasks for the admin view or the "my tasks" view.
func (s *Service) ListTasks(ctx context.Context, filter TaskFilter) ([]TaskSummary, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var (
		items    []TaskSummary
		cursorAt *time.Time
		cursorID string
	)
	if filter.Cursor != "" {
		parts := strings.SplitN(filter.Cursor, "|", 2)
		if len(parts) == 2 {
			if parsed, err := time.Parse(time.RFC3339Nano, parts[0]); err == nil {
				cursorAt = &parsed
				cursorID = parts[1]
			}
		}
	}
	rows, err := s.db.Pool.Query(ctx, `
		select `+taskSelectColumns+`, u.email,
		       coalesce(extract(epoch from (t.finished_at - t.started_at)) * 1000, 0)::bigint
		from ai_tasks t
		join users u on u.id = t.user_id
		where t.deleted_at is null
		  and ($1 = '' or t.user_id = $1::uuid)
		  and ($2 = '' or t.status = $2)
		  and ($3 = '' or t.kind = $3)
		  and ($4::timestamptz is null or (t.created_at, t.id) < ($4::timestamptz, $5::uuid))
		order by t.created_at desc, t.id desc
		limit $6`, filter.UserID, filter.Status, filter.Kind, cursorAt, nullableUUID(cursorID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			summary  TaskSummary
			context  []byte
			attempt  int
			cancel   bool
			started  *time.Time
			finished *time.Time
			duration int64
		)
		if err := rows.Scan(&summary.ID, &summary.Kind, &summary.Status, &context, &attempt, &cancel,
			&summary.ErrorCode, &summary.ErrorText, &summary.CreatedAt, &started, &finished,
			&summary.UserEmail, &duration); err != nil {
			return nil, err
		}
		summary.Context = map[string]any{}
		_ = json.Unmarshal(context, &summary.Context)
		summary.Attempts = attempt
		summary.CancelRequested = cancel
		summary.StartedAt = started
		summary.FinishedAt = finished
		summary.DurationMS = duration
		items = append(items, summary)
	}
	return items, rows.Err()
}

func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// Metrics reports task counts and durations; no cost is invented.
func (s *Service) Metrics(ctx context.Context) (map[string]any, error) {
	var (
		total, completed, failed, cancelled, active, today int
		avgMS                                              float64
	)
	err := s.db.Pool.QueryRow(ctx, `
		select count(*),
		       count(*) filter (where status = 'completed'),
		       count(*) filter (where status = 'failed'),
		       count(*) filter (where status = 'cancelled'),
		       count(*) filter (where status in ('queued','running','waiting_user','cancelling','recovering')),
		       count(*) filter (where created_at >= (date_trunc('day', now() at time zone 'UTC') at time zone 'UTC')),
		       coalesce(avg(extract(epoch from (finished_at - started_at)) * 1000)
		           filter (where finished_at is not null and started_at is not null), 0)
		from ai_tasks where deleted_at is null`).
		Scan(&total, &completed, &failed, &cancelled, &active, &today, &avgMS)
	if err != nil {
		return nil, err
	}
	byKind := map[string]int{}
	rows, err := s.db.Pool.Query(ctx, `select kind, count(*) from ai_tasks where deleted_at is null group by kind`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var kind string
			var count int
			if err := rows.Scan(&kind, &count); err == nil {
				byKind[kind] = count
			}
		}
	}
	return map[string]any{
		"total": total, "completed": completed, "failed": failed, "cancelled": cancelled,
		"active": active, "today": today, "averageDurationMs": int64(avgMS), "byKind": byKind,
	}, nil
}

// ---------------------------------------------------------------- status

// BudgetStatus and ConcurrencyStatus are separate resource signals; neither is
// a model-configuration state.
type BudgetStatus struct {
	DailyLimit int `json:"dailyLimit"`
	UsedToday  int `json:"usedToday"`
}

type ConcurrencyStatus struct {
	Limit  int `json:"limit"`
	Active int `json:"active"`
}

// StatusReport keeps configuration, connection, capability, teaching, budget
// and concurrency distinct so "configured" is never presented as "online".
type StatusReport struct {
	Configured        bool              `json:"configured"`
	GatewayName       string            `json:"gatewayName"`
	Endpoint          string            `json:"endpoint"`
	Model             string            `json:"model"`
	AuthConfigured    bool              `json:"authConfigured"`
	ConnectionStatus  string            `json:"connectionStatus"`
	CapabilityStatus  string            `json:"capabilityStatus"`
	UnavailableReason string            `json:"unavailableReason"`
	Teaching          teaching.Status   `json:"teaching"`
	Budget            BudgetStatus      `json:"budget"`
	Concurrency       ConcurrencyStatus `json:"concurrency"`
}

func (s *Service) Status(ctx context.Context) (StatusReport, error) {
	teachingStatus := teaching.Status{}
	if s.teaching != nil {
		teachingStatus = s.teaching.Status(ctx)
	}
	modelConfigured := s.cfg.AI.Endpoint != "" && s.cfg.AI.Model != ""
	report := StatusReport{
		Configured:        modelConfigured || teachingStatus.Configured,
		GatewayName:       s.cfg.AI.GatewayName,
		Endpoint:          sanitizeEndpoint(s.cfg.AI.Endpoint),
		Model:             s.cfg.AI.Model,
		AuthConfigured:    s.cfg.AI.APIKey != "",
		ConnectionStatus:  "unknown",
		CapabilityStatus:  "unverified",
		UnavailableReason: "",
		Teaching:          teachingStatus,
	}
	if report.GatewayName == "" {
		report.GatewayName = "服务端 AI 网关"
	}
	if !teachingStatus.ExecutionReady {
		report.UnavailableReason = teachingStatus.Reason
	}
	connection, capability, err := s.providerStates(ctx)
	if err != nil {
		return report, nil
	}
	report.ConnectionStatus = connection
	report.CapabilityStatus = capability
	if !modelConfigured && teachingStatus.Configured {
		// When DeepTutor is the only execution backend, its reachability is the
		// meaningful connection signal.
		if teachingStatus.Reachable {
			report.ConnectionStatus = "ok"
		} else {
			report.ConnectionStatus = "failed"
		}
	}
	limit, _ := s.budgetLimit(ctx)
	used, _ := s.usedToday(ctx)
	report.Budget = BudgetStatus{DailyLimit: limit, UsedToday: used}
	concurrencyLimit, _ := s.concurrencyLimit(ctx)
	active, _ := s.activeCount(ctx)
	report.Concurrency = ConcurrencyStatus{Limit: concurrencyLimit, Active: active}
	return report, nil
}

// providerStates reports connection/capability from the admin model config when
// present, otherwise from the most recent real task outcome. A task that
// completed is evidence that the configured endpoint and model stream work;
// no cost figure is ever derived here.
func (s *Service) providerStates(ctx context.Context) (string, string, error) {
	var connection, capability string
	err := s.db.Pool.QueryRow(ctx, `
		select connection_status, capability_status from model_configs
		where kind = 'llm' and is_default = true limit 1`).Scan(&connection, &capability)
	if err == nil {
		return connection, capability, nil
	}
	if s.cfg.AI.Endpoint == "" || s.cfg.AI.Model == "" {
		return "unknown", "unverified", nil
	}
	var (
		status    string
		errorCode string
	)
	err = s.db.Pool.QueryRow(ctx, `
		select status, error_code from ai_tasks
		where status in ('completed', 'failed') and deleted_at is null
		order by finished_at desc nulls last limit 1`).Scan(&status, &errorCode)
	if err != nil {
		return "unknown", "unverified", nil
	}
	switch {
	case status == StatusCompleted:
		return "ok", "verified", nil
	case strings.HasPrefix(errorCode, "model_"), strings.HasPrefix(errorCode, "teaching_"):
		return "failed", "failed", nil
	default:
		return "unknown", "unverified", nil
	}
}

func (s *Service) unavailableReason(ctx context.Context) string {
	if s.teaching == nil {
		return "未配置 AI 教学服务。"
	}
	return s.teaching.UnavailableReason(ctx)
}

// ---------------------------------------------------------------- limits

func (s *Service) budgetLimit(ctx context.Context) (int, error) {
	if value, ok, err := s.settingInt(ctx, "ai.budget.daily"); err != nil {
		return 0, err
	} else if ok {
		return value, nil
	}
	return s.cfg.AI.DailyBudget, nil
}

func (s *Service) concurrencyLimit(ctx context.Context) (int, error) {
	if value, ok, err := s.settingInt(ctx, "ai.concurrency.limit"); err != nil {
		return 0, err
	} else if ok {
		return value, nil
	}
	if s.cfg.AI.MaxConcurrency > 0 {
		return s.cfg.AI.MaxConcurrency, nil
	}
	return 3, nil
}

func (s *Service) settingInt(ctx context.Context, key string) (int, bool, error) {
	var raw []byte
	err := s.db.Pool.QueryRow(ctx, `select value from settings where key = $1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if parsed, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
		return parsed, true, nil
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, false, nil
	}
	return int(number), true, nil
}

func (s *Service) usedToday(ctx context.Context) (int, error) {
	var count int
	// Deleted tasks still consumed budget, so they stay in the daily count to
	// prevent a create-delete loop from bypassing the limit.
	err := s.db.Pool.QueryRow(ctx, `
		select count(*) from ai_invocations
		where created_at >= (date_trunc('day', now() at time zone 'UTC') at time zone 'UTC')`).Scan(&count)
	return count, err
}

func (s *Service) activeCount(ctx context.Context) (int, error) {
	var count int
	err := s.db.Pool.QueryRow(ctx, `
		select count(*) from ai_tasks
		where deleted_at is null and status in ('queued','running','waiting_user','cancelling','recovering')`).Scan(&count)
	return count, err
}

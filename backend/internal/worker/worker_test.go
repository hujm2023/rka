package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rka/internal/app"
	"rka/internal/config"
	"rka/internal/store"
	"rka/internal/teaching"
	"rka/internal/testutil"
)

type workerFixture struct {
	app    *app.App
	server *testutil.Server
	db     *store.DB
	userID string
}

func newWorkerFixture(t *testing.T, mutate func(*config.Config)) *workerFixture {
	t.Helper()
	cfg := testutil.Config(t)
	cfg.Worker.ID = "test-worker"
	cfg.Worker.PollInterval = 10 * time.Millisecond
	cfg.Worker.Heartbeat = 50 * time.Millisecond
	cfg.Worker.LeaseTTL = 5 * time.Second
	cfg.Worker.MaxAttempts = 2
	cfg.Worker.MaxConcurrency = 1
	cfg.AI.RequestTimeout = 3 * time.Second
	if mutate != nil {
		mutate(cfg)
	}
	application, server, db := testutil.NewAppWithConfig(t, cfg)
	email := fmt.Sprintf("worker-%d@example.com", time.Now().UnixNano())
	testutil.CreateUser(t, application, email, "password12345", "learner")
	user := testutil.Login(t, server, email, "password12345")
	return &workerFixture{app: application, server: server, db: db, userID: user.ID}
}

func (f *workerFixture) createTask(t *testing.T, key, prompt string) string {
	t.Helper()
	status, body := f.server.Do(t, http.MethodPost, "/api/ai/tasks", map[string]any{
		"kind": "explain", "idempotencyKey": key,
		"context": map[string]any{"prompt": prompt},
	})
	testutil.MustStatus(t, http.StatusCreated, status, body)
	task, _ := body["task"].(map[string]any)
	id, _ := task["id"].(string)
	if id == "" {
		t.Fatalf("task id missing: %v", body)
	}
	return id
}

func (f *workerFixture) taskRow(t *testing.T, id string) (status, leaseOwner, errorCode string, attempt int) {
	t.Helper()
	err := f.db.Pool.QueryRow(t.Context(), `
		select status, lease_owner, error_code, attempt from ai_tasks where id = $1::uuid`, id).
		Scan(&status, &leaseOwner, &errorCode, &attempt)
	if err != nil {
		t.Fatalf("read task row: %v", err)
	}
	return
}

func (f *workerFixture) waitStatus(t *testing.T, id, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		last, _, _, _ = f.taskRow(t, id)
		if last == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s status = %q, want %q", id, last, want)
}

func (f *workerFixture) eventTypes(t *testing.T, id string) []string {
	t.Helper()
	rows, err := f.db.Pool.Query(t.Context(), `
		select type from ai_events where task_id = $1::uuid order by seq`, id)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var eventType string
		if err := rows.Scan(&eventType); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		types = append(types, eventType)
	}
	return types
}

func writeChatChunks(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	flusher := w.(http.Flusher)
	for _, chunk := range chunks {
		payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": chunk}}}})
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// ---------------------------------------------------------------- success / lease

func TestProcessOneCompletesTaskAndReleasesLease(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatChunks(w, "讲解", "内容")
	}))
	defer upstream.Close()
	fixture := newWorkerFixture(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = upstream.URL
		cfg.AI.Model = "test-model"
	})
	id := fixture.createTask(t, "success", "解释一下")

	processed, err := fixture.app.Worker.ProcessOne(t.Context())
	if err != nil || !processed {
		t.Fatalf("process one = %v, %v", processed, err)
	}
	status, leaseOwner, errorCode, attempt := fixture.taskRow(t, id)
	if status != "completed" || leaseOwner != "" || errorCode != "" || attempt != 1 {
		t.Fatalf("task row = %s/%q/%q/%d", status, leaseOwner, errorCode, attempt)
	}
	types := fixture.eventTypes(t, id)
	if strings.Join(types, ",") != "accepted,delta,delta,done" {
		t.Fatalf("events = %v", types)
	}
}

func TestHeartbeatExtendsLeaseDuringLongTurn(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-release
		writeChatChunks(w, "完成")
	}))
	defer upstream.Close()
	fixture := newWorkerFixture(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = upstream.URL
		cfg.AI.Model = "test-model"
		cfg.Worker.Heartbeat = 30 * time.Millisecond
		cfg.Worker.LeaseTTL = 2 * time.Second
	})
	id := fixture.createTask(t, "heartbeat", "解释")

	done := make(chan error, 1)
	go func() {
		_, err := fixture.app.Worker.ProcessOne(t.Context())
		done <- err
	}()
	fixture.waitStatus(t, id, "running", 3*time.Second)
	var firstLease time.Time
	if err := fixture.db.Pool.QueryRow(t.Context(),
		`select lease_until from ai_tasks where id = $1::uuid`, id).Scan(&firstLease); err != nil {
		t.Fatalf("read lease: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	var secondLease time.Time
	if err := fixture.db.Pool.QueryRow(t.Context(),
		`select lease_until from ai_tasks where id = $1::uuid`, id).Scan(&secondLease); err != nil {
		t.Fatalf("read lease: %v", err)
	}
	if !secondLease.After(firstLease) {
		t.Fatalf("heartbeat did not extend the lease: %v -> %v", firstLease, secondLease)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("process: %v", err)
	}
	fixture.waitStatus(t, id, "completed", 3*time.Second)
}

// ---------------------------------------------------------------- lease recovery

func TestExpiredLeaseMovesToRecoveringThenRuns(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatChunks(w, "恢复后完成")
	}))
	defer upstream.Close()
	fixture := newWorkerFixture(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = upstream.URL
		cfg.AI.Model = "test-model"
	})
	id := fixture.createTask(t, "lease-expired", "解释")

	// Simulate a worker that claimed the task and died before starting a turn.
	claimed, err := fixture.app.AI.ClaimNext(t.Context(), "dead-worker", time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %+v, %v", claimed, err)
	}
	if _, err := fixture.db.Pool.Exec(t.Context(),
		`update ai_tasks set lease_until = now() - interval '1 second' where id = $1::uuid`, id); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	processed, err := fixture.app.Worker.ProcessOne(t.Context())
	if err != nil || !processed {
		t.Fatalf("reap+recover process = %v, %v", processed, err)
	}
	status, leaseOwner, errorCode, _ := fixture.taskRow(t, id)
	if status != "queued" || leaseOwner != "" || errorCode != "upstream_not_started" {
		t.Fatalf("after recovery pass: %s/%q/%q", status, leaseOwner, errorCode)
	}
	time.Sleep(200 * time.Millisecond) // respect the requeue backoff
	processed, err = fixture.app.Worker.ProcessOne(t.Context())
	if err != nil || !processed {
		t.Fatalf("second process = %v, %v", processed, err)
	}
	fixture.waitStatus(t, id, "completed", 3*time.Second)
	if types := fixture.eventTypes(t, id); !contains(types, "done") {
		t.Fatalf("events = %v", types)
	}
}

func TestUnknownUpstreamStateIsRecoveringNotRetriedBlindly(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"半句\"}}]}\n\n")
		flusher.Flush()
		time.Sleep(800 * time.Millisecond)
	}))
	defer upstream.Close()
	fixture := newWorkerFixture(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = upstream.URL
		cfg.AI.Model = "test-model"
		cfg.AI.RequestTimeout = 100 * time.Millisecond
		cfg.Worker.MaxAttempts = 2
	})
	id := fixture.createTask(t, "unknown", "解释")
	if processed, err := fixture.app.Worker.ProcessOne(t.Context()); err != nil || !processed {
		t.Fatalf("first process = %v, %v", processed, err)
	}
	status, _, errorCode, attempt := fixture.taskRow(t, id)
	if status != "recovering" || !strings.HasPrefix(errorCode, "model_") || attempt != 1 {
		t.Fatalf("after unknown outcome: %s/%q/%d", status, errorCode, attempt)
	}
	var teachingTurn string
	if err := fixture.db.Pool.QueryRow(t.Context(),
		`select teaching_turn_id from ai_tasks where id = $1::uuid`, id).Scan(&teachingTurn); err != nil {
		t.Fatalf("read turn: %v", err)
	}
	if teachingTurn == "" {
		t.Fatal("upstream turn id was not recorded for recovery")
	}
	// Second pass verifies the (stateless) gateway again and stops at the retry cap.
	time.Sleep(200 * time.Millisecond)
	if processed, err := fixture.app.Worker.ProcessOne(t.Context()); err != nil || !processed {
		t.Fatalf("recovery process = %v, %v", processed, err)
	}
	status, _, errorCode, attempt = fixture.taskRow(t, id)
	if status != "failed" || errorCode != "upstream_unresolved" || attempt != 2 {
		t.Fatalf("after retry cap: %s/%q/%d", status, errorCode, attempt)
	}
	if types := fixture.eventTypes(t, id); !contains(types, "error") {
		t.Fatalf("events = %v", types)
	}
}

func TestRetryableFailureRequeuesThenFailsAtCap(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream broken", http.StatusInternalServerError)
	}))
	defer upstream.Close()
	fixture := newWorkerFixture(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = upstream.URL
		cfg.AI.Model = "test-model"
		cfg.Worker.MaxAttempts = 2
	})
	id := fixture.createTask(t, "retryable", "解释")
	if processed, err := fixture.app.Worker.ProcessOne(t.Context()); err != nil || !processed {
		t.Fatalf("first process = %v, %v", processed, err)
	}
	status, _, errorCode, attempt := fixture.taskRow(t, id)
	if status != "queued" || errorCode != "model_upstream_error" || attempt != 1 {
		t.Fatalf("after retryable failure: %s/%q/%d", status, errorCode, attempt)
	}
	if types := fixture.eventTypes(t, id); !contains(types, "progress") {
		t.Fatalf("retry was not visible to the browser: %v", types)
	}

	time.Sleep(200 * time.Millisecond)
	if processed, err := fixture.app.Worker.ProcessOne(t.Context()); err != nil || !processed {
		t.Fatalf("second process = %v, %v", processed, err)
	}
	status, _, errorCode, attempt = fixture.taskRow(t, id)
	if status != "failed" || errorCode != "model_upstream_error" || attempt != 2 {
		t.Fatalf("after retry cap: %s/%q/%d", status, errorCode, attempt)
	}
	if types := fixture.eventTypes(t, id); !contains(types, "error") {
		t.Fatalf("events = %v", types)
	}
}

// ---------------------------------------------------------------- cancel

func TestWorkerCancelStopsTurnAndWritesCancelledEvent(t *testing.T) {
	upstreamDone := make(chan struct{})
	upstreamStarted := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(upstreamStarted)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"开始\"}}]}\n\n")
		flusher.Flush()
		ticker := time.NewTicker(30 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				close(upstreamDone)
				return
			case <-ticker.C:
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
					close(upstreamDone)
					return
				}
				flusher.Flush()
			case <-time.After(5 * time.Second):
				return
			}
		}
	}))
	defer upstream.Close()
	fixture := newWorkerFixture(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = upstream.URL
		cfg.AI.Model = "test-model"
	})
	id := fixture.createTask(t, "cancel", "解释")
	done := make(chan error, 1)
	go func() {
		_, err := fixture.app.Worker.ProcessOne(t.Context())
		done <- err
	}()
	fixture.waitStatus(t, id, "running", 3*time.Second)
	select {
	case <-upstreamStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream request did not start")
	}
	if _, err := fixture.app.AI.RequestCancel(t.Context(), fixture.userID, id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("process: %v", err)
	}
	fixture.waitStatus(t, id, "cancelled", 3*time.Second)
	select {
	case <-upstreamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream stream was not cancelled")
	}
	types := fixture.eventTypes(t, id)
	if !contains(types, "cancelled") || contains(types, "done") {
		t.Fatalf("events = %v", types)
	}
}

// ---------------------------------------------------------------- waiting user (DeepTutor)

// fakeDeepTutor emits a question on the first turn and completes on the second.
type fakeDeepTutor struct {
	server   *httptest.Server
	mu       sync.Mutex
	turns    int
	replies  []string
	answered map[string]bool
}

func newFakeDeepTutor(t *testing.T) *fakeDeepTutor {
	t.Helper()
	fake := &fakeDeepTutor{answered: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok","version":"fake-1.0"}`)
	})
	mux.HandleFunc("POST /api/users", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"tu-1"}`)
	})
	mux.HandleFunc("POST /api/turns", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.turns++
		turn := fake.turns
		fake.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"turnId":"turn-%d","sessionId":"session-1"}`, turn)
	})
	mux.HandleFunc("GET /api/turns/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		turnID := r.PathValue("id")
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "event: delta\ndata: {\"text\":\"先想一想。\"}\n\n")
		flusher.Flush()
		fake.mu.Lock()
		answered := fake.answered[turnID]
		fake.mu.Unlock()
		if turnID == "turn-1" && !answered {
			_, _ = io.WriteString(w, "event: question\ndata: {\"text\":\"你认为关键在哪里？\"}\n\n")
		} else {
			_, _ = io.WriteString(w, "event: done\ndata: {\"status\":\"completed\"}\n\n")
		}
		flusher.Flush()
	})
	mux.HandleFunc("POST /api/turns/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Content string `json:"content"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		fake.mu.Lock()
		fake.replies = append(fake.replies, payload.Content)
		fake.answered[r.PathValue("id")] = true
		fake.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/turns/{id}/cancel", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /api/turns/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"completed","output":"恢复文本"}`)
	})
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func TestWaitingUserThenReplyCompletes(t *testing.T) {
	fake := newFakeDeepTutor(t)
	fixture := newWorkerFixture(t, func(cfg *config.Config) {
		cfg.Teaching.DeepTutorURL = fake.server.URL
		cfg.Teaching.DeepTutorToken = "token"
	})
	id := fixture.createTask(t, "waiting", "给我一个提示")

	if processed, err := fixture.app.Worker.ProcessOne(t.Context()); err != nil || !processed {
		t.Fatalf("first process = %v, %v", processed, err)
	}
	fixture.waitStatus(t, id, "waiting_user", 3*time.Second)
	types := fixture.eventTypes(t, id)
	if !contains(types, "question") {
		t.Fatalf("events = %v", types)
	}

	status, body := fixture.server.Do(t, http.MethodPost, "/api/ai/tasks/"+id+"/messages", map[string]any{"content": "我觉得是分层"})
	testutil.MustStatus(t, http.StatusOK, status, body)
	if processed, err := fixture.app.Worker.ProcessOne(t.Context()); err != nil || !processed {
		t.Fatalf("second process = %v, %v", processed, err)
	}
	fixture.waitStatus(t, id, "completed", 3*time.Second)
	fake.mu.Lock()
	replies := append([]string(nil), fake.replies...)
	turns := fake.turns
	fake.mu.Unlock()
	if len(replies) != 1 || replies[0] != "我觉得是分层" {
		t.Fatalf("deeptutor replies = %v", replies)
	}
	if turns != 1 {
		t.Fatalf("deeptutor created a new turn instead of resuming: %d", turns)
	}
}

// ---------------------------------------------------------------- Run loop

func TestRunProcessesQueuedTasksAndStops(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeChatChunks(w, "完成")
	}))
	defer upstream.Close()
	fixture := newWorkerFixture(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = upstream.URL
		cfg.AI.Model = "test-model"
	})
	first := fixture.createTask(t, "run-1", "第一个")
	second := fixture.createTask(t, "run-2", "第二个")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fixture.app.Worker.Run(ctx) }()
	fixture.waitStatus(t, first, "completed", 5*time.Second)
	fixture.waitStatus(t, second, "completed", 5*time.Second)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker run: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not stop after context cancellation")
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestTurnIdentityPersistsBeforeStreamCompletes(t *testing.T) {
	accepted := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		w.(http.Flusher).Flush()
		close(accepted)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer upstream.Close()
	fixture := newWorkerFixture(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = upstream.URL
		cfg.AI.Model = "review-only"
		cfg.AI.RequestTimeout = 10 * time.Second
	})
	id := fixture.createTask(t, "review-stream-turn", "explain")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { fixture.app.Worker.ProcessOne(ctx); close(done) }()
	<-accepted
	var turn string
	err := fixture.db.Pool.QueryRow(t.Context(), `select teaching_turn_id from ai_tasks where id=$1::uuid`, id).Scan(&turn)
	once.Do(func() { close(release) })
	<-done
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("upstream accepted streaming request; persisted turn id during stream=%q", turn)
	if turn == "" {
		t.Fatal("upstream accepted request while DB recovery identity remains empty; crash would requeue without recovering upstream")
	}
}

func TestAcceptedRequestWithoutResponseIsNotRetried(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer upstream.Close()
	f := newWorkerFixture(t, func(cfg *config.Config) { cfg.AI.Endpoint = upstream.URL; cfg.AI.Model = "test" })
	id := f.createTask(t, "ambiguous-acceptance", "explain")
	for i := 0; i < 3; i++ {
		if i > 0 {
			if _, err := f.db.Pool.Exec(t.Context(), `update ai_tasks set lease_until=now()-interval '1 second' where id=$1::uuid`, id); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := f.app.Worker.ProcessOne(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("ambiguous accepted request was sent %d times", calls.Load())
	}
}

func TestRecoveryRestoresCommittedQuestionWithoutCallingUpstream(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer upstream.Close()
	f := newWorkerFixture(t, func(cfg *config.Config) { cfg.AI.Endpoint = upstream.URL; cfg.AI.Model = "test" })
	id := f.createTask(t, "question-recovery", "explain")
	claimed, err := f.app.AI.ClaimNext(t.Context(), "old-worker", time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("claim %v", err)
	}
	if err := f.app.AI.SetTeachingTurn(t.Context(), id, "old-worker", "turn-known", "session-known"); err != nil {
		t.Fatal(err)
	}
	if err := f.app.AI.AppendFrame(t.Context(), id, "old-worker", 4, []teaching.Event{{Type: "delta", Payload: map[string]any{"text": "先考虑约束。"}}, {Type: "question", Payload: map[string]any{"text": "哪个约束最重要？"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `update ai_tasks set lease_until=now()-interval '1 second' where id=$1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Worker.ProcessOne(t.Context()); err != nil {
		t.Fatal(err)
	}
	task, err := f.app.AI.TaskForUser(t.Context(), f.userID, id)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != "waiting_user" {
		t.Fatalf("recovered state=%s", task.Status)
	}
	if calls.Load() != 0 {
		t.Fatal("recovery repeated an upstream invocation")
	}
	history, err := f.app.AI.Conversation(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) == 0 || !strings.Contains(history[len(history)-1].Content, "哪个约束最重要") {
		t.Fatalf("question missing from conversation: %v", history)
	}
}

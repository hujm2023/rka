package ai_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"rka/internal/app"
	"rka/internal/config"
	"rka/internal/store"
	"rka/internal/teaching"
	"rka/internal/testutil"
)

// ---------------------------------------------------------------- helpers

type aiFixture struct {
	app     *app.App
	server  *testutil.Server
	db      *store.DB
	userID  string
	email   string
	upsteam *httptest.Server
}

func newAIFixture(t *testing.T, mutate func(*config.Config)) *aiFixture {
	t.Helper()
	cfg := testutil.Config(t)
	cfg.Worker.PollInterval = 10 * time.Millisecond
	cfg.Worker.Heartbeat = 50 * time.Millisecond
	cfg.Worker.LeaseTTL = 20 * time.Second
	cfg.Worker.MaxAttempts = 3
	cfg.AI.RequestTimeout = 5 * time.Second
	cfg.AI.Endpoint = ""
	cfg.AI.Model = ""
	if mutate != nil {
		mutate(cfg)
	}
	application, server, db := testutil.NewAppWithConfig(t, cfg)
	email := fmt.Sprintf("learner-%d@example.com", time.Now().UnixNano())
	testutil.CreateUser(t, application, email, "password12345", "learner")
	user := testutil.Login(t, server, email, "password12345")
	return &aiFixture{app: application, server: server, db: db, userID: user.ID, email: email}
}

func (f *aiFixture) close() {
	if f.upsteam != nil {
		f.upsteam.Close()
	}
}

// withChatUpstream wires an OpenAI-compatible fake upstream into a fresh
// fixture. No real model call is ever made.
func withChatUpstream(t *testing.T, handler http.HandlerFunc) *aiFixture {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	fixture := newAIFixture(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = upstream.URL + "/v1/chat/completions"
		cfg.AI.Model = "test-model"
		cfg.AI.APIKey = "test-key"
	})
	fixture.upsteam = upstream
	return fixture
}

func (f *aiFixture) createTask(t *testing.T, key, kind, prompt string) (int, map[string]any) {
	t.Helper()
	return f.server.Do(t, http.MethodPost, "/api/ai/tasks", map[string]any{
		"kind": kind, "idempotencyKey": key,
		"context": map[string]any{"prompt": prompt, "hint": kind == "hint"},
	})
}

func taskFrom(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	task, ok := body["task"].(map[string]any)
	if !ok {
		t.Fatalf("response has no task: %v", body)
	}
	return task
}

func taskID(t *testing.T, body map[string]any) string {
	t.Helper()
	id, _ := taskFrom(t, body)["id"].(string)
	if id == "" {
		t.Fatalf("task has no id: %v", body)
	}
	return id
}

func errorCode(body map[string]any) string {
	envelope, _ := body["error"].(map[string]any)
	code, _ := envelope["code"].(string)
	return code
}

func errorMessage(body map[string]any) string {
	envelope, _ := body["error"].(map[string]any)
	message, _ := envelope["message"].(string)
	return message
}

func (f *aiFixture) taskStatus(t *testing.T, id string) string {
	t.Helper()
	var status string
	if err := f.db.Pool.QueryRow(t.Context(), `select status from ai_tasks where id = $1::uuid`, id).Scan(&status); err != nil {
		t.Fatalf("read task status: %v", err)
	}
	return status
}

func (f *aiFixture) waitStatus(t *testing.T, id, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for time.Now().Before(deadline) {
		last = f.taskStatus(t, id)
		if last == want {
			return last
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s status = %q, want %q", id, last, want)
	return last
}

type sseEvent struct {
	ID   string
	Name string
	Data map[string]any
	Raw  string
}

// readSSE consumes a server-sent event stream until the handler closes it.
func readSSE(t *testing.T, client *http.Client, url string, headers map[string]string) []sseEvent {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build SSE request: %v", err)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("open SSE stream: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("SSE status = %d, body=%s", response.StatusCode, raw)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("SSE content type = %q", contentType)
	}
	var events []sseEvent
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	current := sseEvent{}
	var dataLines []string
	flush := func() {
		if current.ID == "" && len(dataLines) == 0 {
			return
		}
		current.Raw = strings.Join(dataLines, "\n")
		if len(dataLines) > 0 {
			decoded := map[string]any{}
			if err := json.Unmarshal([]byte(current.Raw), &decoded); err == nil {
				current.Data = decoded
			}
		}
		events = append(events, current)
		current = sseEvent{}
		dataLines = nil
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "id":
			current.ID = value
		case "event":
			current.Name = value
		case "data":
			dataLines = append(dataLines, value)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read SSE: %v", err)
	}
	flush()
	return events
}

func eventTypes(events []sseEvent) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, fmt.Sprint(event.Data["type"]))
	}
	return types
}

func hasEvent(events []sseEvent, want string) bool {
	for _, event := range events {
		if event.Data["type"] == want {
			return true
		}
	}
	return false
}

func writeChatSSE(t *testing.T, w http.ResponseWriter, chunks ...string) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, ok := w.(http.Flusher)
	if !ok {
		t.Error("fake upstream does not support flush")
		return
	}
	for _, chunk := range chunks {
		payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": chunk}}}})
		fmt.Fprintf(w, "data: %s\n\n", payload)
		flusher.Flush()
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// ---------------------------------------------------------------- status

func TestStatusUnavailableAndCreateReturns503(t *testing.T) {
	fixture := newAIFixture(t, nil)

	status, body := fixture.server.Do(t, http.MethodGet, "/api/ai/status", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["configured"] != false {
		t.Fatalf("configured = %v", body["configured"])
	}
	if reason, _ := body["unavailableReason"].(string); reason == "" {
		t.Fatal("unavailableReason is empty")
	}
	if body["connectionStatus"] != "unknown" || body["capabilityStatus"] != "unverified" {
		t.Fatalf("connection/capability not distinct: %v", body)
	}
	teaching, _ := body["teaching"].(map[string]any)
	if teaching == nil || teaching["configured"] != false || teaching["reason"] == "" {
		t.Fatalf("teaching status missing: %v", body["teaching"])
	}
	if _, ok := body["budget"].(map[string]any); !ok {
		t.Fatal("budget missing")
	}
	if _, ok := body["concurrency"].(map[string]any); !ok {
		t.Fatal("concurrency missing")
	}

	status, body = fixture.createTask(t, "key-unavailable", "hint", "给我提示")
	testutil.MustStatus(t, http.StatusServiceUnavailable, status, body)
	if errorCode(body) != "ai_unavailable" || errorMessage(body) == "" {
		t.Fatalf("error envelope = %v", body)
	}
}

func TestStatusReportsConfiguredGateway(t *testing.T) {
	fixture := withChatUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatSSE(t, w, "好")
	})
	status, body := fixture.server.Do(t, http.MethodGet, "/api/ai/status", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["configured"] != true || body["model"] != "test-model" || body["authConfigured"] != true {
		t.Fatalf("status = %v", body)
	}
	endpoint, _ := body["endpoint"].(string)
	if !strings.HasPrefix(endpoint, "http://127.0.0.1:") || strings.Contains(endpoint, "test-key") {
		t.Fatalf("endpoint = %q", endpoint)
	}
	if body["unavailableReason"] != "" {
		t.Fatalf("unavailableReason = %v", body["unavailableReason"])
	}
}

// ---------------------------------------------------------------- create / list

func TestCreateTaskIsIdempotentAndListed(t *testing.T) {
	fixture := withChatUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatSSE(t, w, "回答")
	})
	firstStatus, firstBody := fixture.createTask(t, "key-1", "hint", "给我提示")
	testutil.MustStatus(t, http.StatusCreated, firstStatus, firstBody)
	firstID := taskID(t, firstBody)

	replayStatus, replayBody := fixture.createTask(t, "key-1", "hint", "给我提示")
	testutil.MustStatus(t, http.StatusOK, replayStatus, replayBody)
	if taskID(t, replayBody) != firstID {
		t.Fatal("idempotent replay returned a different task")
	}

	differentStatus, differentBody := fixture.createTask(t, "key-2", "explain", "解释一下")
	testutil.MustStatus(t, http.StatusCreated, differentStatus, differentBody)
	if taskID(t, differentBody) == firstID {
		t.Fatal("different idempotency key reused a task")
	}

	status, body := fixture.server.Do(t, http.MethodGet, "/api/ai/tasks", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("task list = %v", body)
	}

	status, body = fixture.server.Do(t, http.MethodGet, "/api/ai/tasks/"+firstID, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if taskFrom(t, body)["status"] != "queued" {
		t.Fatalf("task = %v", body)
	}
	events, _ := body["events"].([]any)
	if len(events) == 0 {
		t.Fatal("accepted event missing")
	}
	firstEvent, _ := events[0].(map[string]any)
	if firstEvent["type"] != "accepted" || firstEvent["seq"] != float64(1) {
		t.Fatalf("first event = %v", firstEvent)
	}
}

func TestListTasksPagination(t *testing.T) {
	fixture := withChatUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatSSE(t, w, "回答")
	})
	for i, key := range []string{"page-1", "page-2", "page-3"} {
		status, body := fixture.createTask(t, key, "explain", fmt.Sprintf("问题 %d", i))
		testutil.MustStatus(t, http.StatusCreated, status, body)
	}
	status, body := fixture.server.Do(t, http.MethodGet, "/api/ai/tasks?limit=2", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("first page = %v", body)
	}
	cursor, _ := body["nextCursor"].(string)
	if cursor == "" {
		t.Fatal("nextCursor missing")
	}
	status, body = fixture.server.Do(t, http.MethodGet, "/api/ai/tasks?limit=2&cursor="+url.QueryEscape(cursor), nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	second, _ := body["items"].([]any)
	if len(second) != 1 {
		t.Fatalf("second page = %v", body)
	}
	if first, _ := items[0].(map[string]any); first["id"] == second[0].(map[string]any)["id"] {
		t.Fatal("pagination repeated the first item")
	}
}

// ---------------------------------------------------------------- lifecycle and SSE

func TestTaskLifecycleAndEventReplay(t *testing.T) {
	fixture := withChatUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatSSE(t, w, "第一段，", "第二段 [S0]。")
	})
	status, body := fixture.createTask(t, "lifecycle", "explain", "解释这个知识点")
	testutil.MustStatus(t, http.StatusCreated, status, body)
	id := taskID(t, body)

	processed, err := fixture.app.Worker.ProcessOne(t.Context())
	if err != nil || !processed {
		t.Fatalf("process one = %v, %v", processed, err)
	}
	fixture.waitStatus(t, id, "completed", 3*time.Second)

	status, body = fixture.server.Do(t, http.MethodGet, "/api/ai/tasks/"+id, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	task := taskFrom(t, body)
	if task["status"] != "completed" || task["finishedAt"] == nil {
		t.Fatalf("task = %v", task)
	}

	events := readSSE(t, fixture.server.Client, fixture.server.URL+"/api/ai/tasks/"+id+"/events?after=0", nil)
	if len(events) < 4 {
		t.Fatalf("events = %v", eventTypes(events))
	}
	if events[0].Data["type"] != "accepted" {
		t.Fatalf("first event = %v", events[0])
	}
	last := events[len(events)-1]
	if last.Data["type"] != "done" {
		t.Fatalf("last event = %v", last.Data)
	}
	donePayload, _ := last.Data["payload"].(map[string]any)
	if donePayload["status"] != "completed" {
		t.Fatalf("done payload = %v", donePayload)
	}
	lastSeq := 0
	for _, event := range events {
		seq := int(event.Data["seq"].(float64))
		if seq <= lastSeq {
			t.Fatalf("seq not monotonic: %v", eventTypes(events))
		}
		lastSeq = seq
	}

	// Replay after a sequence number returns only newer events.
	afterSeq := 1
	replay := readSSE(t, fixture.server.Client,
		fmt.Sprintf("%s/api/ai/tasks/%s/events?after=%d", fixture.server.URL, id, afterSeq), nil)
	if len(replay) != len(events)-1 {
		t.Fatalf("replay length = %d, want %d", len(replay), len(events)-1)
	}
	if int(replay[0].Data["seq"].(float64)) != afterSeq+1 {
		t.Fatalf("replay starts at %v", replay[0].Data["seq"])
	}

	// Last-Event-ID is honoured when ?after is absent.
	resume := readSSE(t, fixture.server.Client, fixture.server.URL+"/api/ai/tasks/"+id+"/events",
		map[string]string{"Last-Event-ID": fmt.Sprint(lastSeq - 1)})
	if len(resume) != 1 || resume[0].Data["type"] != "done" {
		t.Fatalf("Last-Event-ID replay = %v", eventTypes(resume))
	}
}

func TestSSEStreamsLiveEventsWhileWorkerRuns(t *testing.T) {
	release := make(chan struct{})
	fixture := withChatUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"思考中\"}}]}\n\n")
		flusher.Flush()
		<-release
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"，完成。\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	})
	status, body := fixture.createTask(t, "live", "explain", "解释")
	testutil.MustStatus(t, http.StatusCreated, status, body)
	id := taskID(t, body)

	done := make(chan []sseEvent, 1)
	go func() {
		done <- readSSE(t, fixture.server.Client, fixture.server.URL+"/api/ai/tasks/"+id+"/events?after=0", nil)
	}()
	workerDone := make(chan error, 1)
	go func() {
		_, err := fixture.app.Worker.ProcessOne(t.Context())
		workerDone <- err
	}()
	fixture.waitStatus(t, id, "running", 3*time.Second)
	close(release)
	if err := <-workerDone; err != nil {
		t.Fatalf("worker: %v", err)
	}
	select {
	case events := <-done:
		if !hasEvent(events, "done") || !hasEvent(events, "delta") {
			t.Fatalf("live events = %v", eventTypes(events))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SSE stream did not close after completion")
	}
}

// ---------------------------------------------------------------- cancel / messages / delete

func TestCancelQueuedTask(t *testing.T) {
	fixture := withChatUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatSSE(t, w, "不应执行")
	})
	status, body := fixture.createTask(t, "cancel-queued", "explain", "解释")
	testutil.MustStatus(t, http.StatusCreated, status, body)
	id := taskID(t, body)

	status, body = fixture.server.Do(t, http.MethodPost, "/api/ai/tasks/"+id+"/cancel", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if taskFrom(t, body)["status"] != "cancelled" {
		t.Fatalf("task = %v", body)
	}
	events := readSSE(t, fixture.server.Client, fixture.server.URL+"/api/ai/tasks/"+id+"/events?after=0", nil)
	if !hasEvent(events, "cancelled") || hasEvent(events, "done") {
		t.Fatalf("events = %v", eventTypes(events))
	}
	// Cancelling again is idempotent.
	status, body = fixture.server.Do(t, http.MethodPost, "/api/ai/tasks/"+id+"/cancel", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
}

func TestCancelRunningTaskStopsUpstream(t *testing.T) {
	upstreamDone := make(chan struct{})
	upstreamStarted := make(chan struct{})
	fixture := withChatUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		close(upstreamStarted)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"开始\"}}]}\n\n")
		flusher.Flush()
		ticker := time.NewTicker(50 * time.Millisecond)
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
	})
	status, body := fixture.createTask(t, "cancel-running", "explain", "解释")
	testutil.MustStatus(t, http.StatusCreated, status, body)
	id := taskID(t, body)

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		_, _ = fixture.app.Worker.ProcessOne(t.Context())
	}()
	fixture.waitStatus(t, id, "running", 3*time.Second)
	select {
	case <-upstreamStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream request did not start")
	}
	status, body = fixture.server.Do(t, http.MethodPost, "/api/ai/tasks/"+id+"/cancel", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	select {
	case <-workerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after cancel")
	}
	fixture.waitStatus(t, id, "cancelled", 3*time.Second)
	select {
	case <-upstreamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream connection was not closed after cancel")
	}
	events := readSSE(t, fixture.server.Client, fixture.server.URL+"/api/ai/tasks/"+id+"/events?after=0", nil)
	if !hasEvent(events, "cancelled") || hasEvent(events, "done") {
		t.Fatalf("events = %v", eventTypes(events))
	}
}

func TestFollowupMessageRunsSecondTurn(t *testing.T) {
	var requests int
	var mu sync.Mutex
	fixture := withChatUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if len(payload.Messages) < 2 {
			t.Errorf("follow-up request has no history: %v", payload)
		}
		writeChatSSE(t, w, "回答")
	})
	status, body := fixture.createTask(t, "followup", "explain", "第一次提问")
	testutil.MustStatus(t, http.StatusCreated, status, body)
	id := taskID(t, body)
	if _, err := fixture.app.Worker.ProcessOne(t.Context()); err != nil {
		t.Fatalf("first turn: %v", err)
	}
	fixture.waitStatus(t, id, "completed", 3*time.Second)

	status, body = fixture.server.Do(t, http.MethodPost, "/api/ai/tasks/"+id+"/messages", map[string]any{"content": "再解释一点"})
	testutil.MustStatus(t, http.StatusOK, status, body)
	if taskFrom(t, body)["status"] != "queued" {
		t.Fatalf("task after message = %v", body)
	}
	if _, err := fixture.app.Worker.ProcessOne(t.Context()); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	fixture.waitStatus(t, id, "completed", 3*time.Second)
	mu.Lock()
	count := requests
	mu.Unlock()
	if count != 2 {
		t.Fatalf("upstream requests = %d", count)
	}
	var messages int
	if err := fixture.db.Pool.QueryRow(t.Context(),
		`select count(*) from ai_messages where task_id = $1::uuid`, id).Scan(&messages); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if messages != 4 {
		t.Fatalf("persisted messages = %d, want 4", messages)
	}
	// Message on a running task conflicts rather than corrupting the turn.
}

func TestMessageOnRunningTaskConflicts(t *testing.T) {
	release := make(chan struct{})
	fixture := withChatUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-release
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"完成\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	})
	status, body := fixture.createTask(t, "busy", "explain", "解释")
	testutil.MustStatus(t, http.StatusCreated, status, body)
	id := taskID(t, body)
	go func() { _, _ = fixture.app.Worker.ProcessOne(t.Context()) }()
	fixture.waitStatus(t, id, "running", 3*time.Second)
	status, body = fixture.server.Do(t, http.MethodPost, "/api/ai/tasks/"+id+"/messages", map[string]any{"content": "追问"})
	testutil.MustStatus(t, http.StatusConflict, status, body)
	if errorCode(body) != "task_busy" {
		t.Fatalf("error = %v", body)
	}
	close(release)
	fixture.waitStatus(t, id, "completed", 3*time.Second)
}

func TestDeleteTaskSoftDeletes(t *testing.T) {
	fixture := withChatUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatSSE(t, w, "回答")
	})
	status, body := fixture.createTask(t, "delete", "explain", "解释")
	testutil.MustStatus(t, http.StatusCreated, status, body)
	id := taskID(t, body)
	// A running/queued task cannot be deleted; finish it first.
	if processed, err := fixture.app.Worker.ProcessOne(t.Context()); err != nil || !processed {
		t.Fatalf("process: %v, %v", processed, err)
	}
	fixture.waitStatus(t, id, "completed", 3*time.Second)

	status, body = fixture.server.Do(t, http.MethodDelete, "/api/ai/tasks/"+id, nil)
	testutil.MustStatus(t, http.StatusNoContent, status, body)
	status, _ = fixture.server.Do(t, http.MethodGet, "/api/ai/tasks/"+id, nil)
	if status != http.StatusNotFound {
		t.Fatalf("deleted task status = %d", status)
	}
	_, listBody := fixture.server.Do(t, http.MethodGet, "/api/ai/tasks", nil)
	if items, _ := listBody["items"].([]any); len(items) != 0 {
		t.Fatalf("deleted task still listed: %v", listBody)
	}
}

// ---------------------------------------------------------------- quota

func TestDailyBudgetLimit(t *testing.T) {
	fixture := newAIFixture(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = "http://127.0.0.1:1"
		cfg.AI.Model = "test-model"
		cfg.AI.DailyBudget = 1
	})
	status, body := fixture.createTask(t, "budget-1", "explain", "第一次")
	testutil.MustStatus(t, http.StatusCreated, status, body)
	firstID := taskID(t, body)

	status, body = fixture.createTask(t, "budget-2", "explain", "第二次")
	testutil.MustStatus(t, http.StatusTooManyRequests, status, body)
	if errorCode(body) != "ai_budget_exceeded" {
		t.Fatalf("error = %v", body)
	}
	// An idempotent replay still returns the original task.
	status, body = fixture.createTask(t, "budget-1", "explain", "第一次")
	testutil.MustStatus(t, http.StatusOK, status, body)
	if taskID(t, body) != firstID {
		t.Fatal("budget check broke idempotency")
	}
}

func TestConcurrencyLimit(t *testing.T) {
	fixture := newAIFixture(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = "http://127.0.0.1:1"
		cfg.AI.Model = "test-model"
		cfg.AI.MaxConcurrency = 1
	})
	status, body := fixture.createTask(t, "conc-1", "explain", "第一个")
	testutil.MustStatus(t, http.StatusCreated, status, body)
	firstID := taskID(t, body)

	status, body = fixture.createTask(t, "conc-2", "explain", "第二个")
	testutil.MustStatus(t, http.StatusTooManyRequests, status, body)
	if errorCode(body) != "ai_concurrency_limit" {
		t.Fatalf("error = %v", body)
	}
	// Cancelling the active task frees the slot.
	if status, body = fixture.server.Do(t, http.MethodPost, "/api/ai/tasks/"+firstID+"/cancel", nil); status != http.StatusOK {
		t.Fatalf("cancel = %d %v", status, body)
	}
	status, body = fixture.createTask(t, "conc-3", "explain", "第三个")
	testutil.MustStatus(t, http.StatusCreated, status, body)
}

// ---------------------------------------------------------------- isolation

func TestTaskIsolationBetweenUsers(t *testing.T) {
	fixture := withChatUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatSSE(t, w, "回答")
	})
	status, body := fixture.createTask(t, "private", "explain", "私密问题")
	testutil.MustStatus(t, http.StatusCreated, status, body)
	id := taskID(t, body)

	otherServer := testutil.NewServer(t, fixture.app.Handler)
	otherEmail := "other@example.com"
	testutil.CreateUser(t, fixture.app, otherEmail, "password12345", "learner")
	testutil.Login(t, otherServer, otherEmail, "password12345")

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/ai/tasks/" + id},
		{http.MethodGet, "/api/ai/tasks/" + id + "/events?after=0"},
		{http.MethodPost, "/api/ai/tasks/" + id + "/cancel"},
		{http.MethodDelete, "/api/ai/tasks/" + id},
	} {
		status, body := otherServer.Do(t, route.method, route.path, nil)
		if status != http.StatusNotFound {
			t.Fatalf("%s %s = %d %v, want 404", route.method, route.path, status, body)
		}
	}
}

// ---------------------------------------------------------------- metrics

func TestMetricsDoNotInventCost(t *testing.T) {
	fixture := withChatUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		writeChatSSE(t, w, "回答")
	})
	if status, body := fixture.createTask(t, "metric", "explain", "解释"); status != http.StatusCreated {
		t.Fatalf("create = %d %v", status, body)
	}
	metrics, err := fixture.app.AI.Metrics(context.Background())
	if err != nil {
		t.Fatalf("metrics: %v", err)
	}
	if _, hasCost := metrics["cost"]; hasCost {
		t.Fatal("metrics must not fabricate cost")
	}
	if metrics["total"] != 1 {
		t.Fatalf("metrics = %v", metrics)
	}
}

func TestFollowupsReserveBudgetAcrossDays(t *testing.T) {
	f := newAIFixture(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = "http://127.0.0.1:1"
		cfg.AI.Model = "test"
		cfg.AI.DailyBudget = 1
	})
	status, body := f.createTask(t, "budget-followup", "explain", "hello")
	testutil.MustStatus(t, http.StatusCreated, status, body)
	id := taskID(t, body)
	task, err := f.app.AI.ClaimNext(t.Context(), "quota-test", time.Minute)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := f.app.AI.CompleteTask(t.Context(), id, "quota-test", teaching.Result{FinalText: "done"}); err != nil {
		t.Fatal(err)
	}
	status, body = f.server.Do(t, http.MethodPost, "/api/ai/tasks/"+id+"/messages", map[string]any{"content": "followup"})
	testutil.MustStatus(t, http.StatusTooManyRequests, status, body)
	// Move the invocation, not merely the task, to the previous accounting day.
	if _, err := f.db.Pool.Exec(t.Context(), `update ai_invocations set created_at=now()-interval '2 days'`); err != nil {
		t.Fatal(err)
	}
	status, body = f.server.Do(t, http.MethodPost, "/api/ai/tasks/"+id+"/messages", map[string]any{"content": "today"})
	testutil.MustStatus(t, http.StatusOK, status, body)
	task, err = f.app.AI.ClaimNext(t.Context(), "quota-test", time.Minute)
	if err != nil || task == nil {
		t.Fatalf("claim followup: %v", err)
	}
	if _, err := f.app.AI.CompleteTask(t.Context(), id, "quota-test", teaching.Result{FinalText: "done"}); err != nil {
		t.Fatal(err)
	}
	status, body = f.server.Do(t, http.MethodPost, "/api/ai/tasks/"+id+"/messages", map[string]any{"content": "another today"})
	testutil.MustStatus(t, http.StatusTooManyRequests, status, body)
}

func TestClearingLearningDataDoesNotRefundBudget(t *testing.T) {
	f := newAIFixture(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = "http://127.0.0.1:1"
		cfg.AI.Model = "test"
		cfg.AI.DailyBudget = 1
	})
	code, body := f.createTask(t, "before-delete", "explain", "hello")
	testutil.MustStatus(t, 201, code, body)
	code, body = f.server.Do(t, http.MethodDelete, "/api/me/data", map[string]any{"confirm": "DELETE"})
	testutil.MustStatus(t, 200, code, body)
	code, body = f.createTask(t, "after-delete", "explain", "again")
	testutil.MustStatus(t, 429, code, body)
	if errorCode(body) != "ai_budget_exceeded" {
		t.Fatalf("quota was refunded: %v", body)
	}
}

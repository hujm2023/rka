package teaching_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"rka/internal/config"
	"rka/internal/store"
	"rka/internal/teaching"
	"rka/internal/testutil"
)

// newTestService builds a teaching service against a real, isolated database.
func newTestService(t *testing.T, mutate func(*config.Config)) (*teaching.Service, *store.DB) {
	t.Helper()
	db := testutil.NewDatabase(t)
	cfg := testutil.Config(t)
	if mutate != nil {
		mutate(cfg)
	}
	return teaching.New(teaching.Deps{Config: cfg, DB: db, Log: testutil.Logger()}), db
}

func seedUser(t *testing.T, db *store.DB, email string) string {
	t.Helper()
	var id string
	err := db.Pool.QueryRow(t.Context(), `
		insert into users (email, display_name, role, password_hash)
		values ($1, '测试用户', 'learner', 'unused') returning id::text`, email).Scan(&id)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return id
}

func seedQuestion(t *testing.T, db *store.DB, stem, answer, analysis string) string {
	t.Helper()
	const questionID = "q-test-001"
	_, err := db.Pool.Exec(t.Context(), `
		insert into questions (id, source_type, term, paper, question_no, module, knowledge)
		values ($1, 'mock', '2026年5月 模拟卷1', '模拟卷1', 1, 'architecture', '架构风格')
		on conflict (id) do nothing`, questionID)
	if err != nil {
		t.Fatalf("seed question: %v", err)
	}
	var versionID string
	err = db.Pool.QueryRow(t.Context(), `
		insert into question_versions (question_id, revision, stem, options, answer, analysis)
		values ($1, 'r1', $2, '{"A":"选项一","B":"选项二","C":"选项三","D":"选项四"}', $3, $4)
		on conflict (question_id, revision) do update set stem = excluded.stem
		returning id::text`, questionID, stem, answer, analysis).Scan(&versionID)
	if err != nil {
		t.Fatalf("seed question version: %v", err)
	}
	if _, err := db.Pool.Exec(t.Context(),
		`update questions set current_version_id = $1::uuid where id = $2`, versionID, questionID); err != nil {
		t.Fatalf("attach question version: %v", err)
	}
	return questionID
}

func seedAttempt(t *testing.T, db *store.DB, userID, questionID, selected string) {
	t.Helper()
	if _, err := db.Pool.Exec(t.Context(), `
		insert into attempts (id, user_id, question_id, question_revision, selected, correct, answered_at)
		values ('attempt-' || gen_random_uuid()::text, $1::uuid, $2, 'r1', $3, true, now())`,
		userID, questionID, selected); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
}

// ---------------------------------------------------------------- identity

func TestIdentityIsPerUserEncryptedAndStable(t *testing.T) {
	service, db := newTestService(t, nil)
	userA := seedUser(t, db, "a@example.com")
	userB := seedUser(t, db, "b@example.com")

	identityA, err := service.Identity(t.Context(), userA)
	if err != nil {
		t.Fatalf("identity A: %v", err)
	}
	identityA2, err := service.Identity(t.Context(), userA)
	if err != nil || identityA2.TeachingUserID != identityA.TeachingUserID || identityA2.Credential != identityA.Credential {
		t.Fatalf("identity is not stable: %+v / %v", identityA2, err)
	}
	identityB, err := service.Identity(t.Context(), userB)
	if err != nil {
		t.Fatalf("identity B: %v", err)
	}
	if identityA.TeachingUserID == identityB.TeachingUserID || identityA.Credential == identityB.Credential {
		t.Fatal("users share a teaching identity")
	}
	if identityA.Credential == "" || identityA.Credential == "unused" {
		t.Fatal("identity credential was not generated")
	}
	var stored string
	if err := db.Pool.QueryRow(t.Context(),
		`select credential_ciphertext from teaching_identities where user_id = $1::uuid`, userA).Scan(&stored); err != nil {
		t.Fatalf("read stored credential: %v", err)
	}
	if stored == identityA.Credential || strings.Contains(stored, identityA.Credential) {
		t.Fatal("credential stored in plaintext")
	}
}

// ---------------------------------------------------------------- status

func TestStatusUnavailableWithoutConfiguration(t *testing.T) {
	service, _ := newTestService(t, nil)
	status := service.Status(t.Context())
	if status.Configured || status.Reachable || status.ExecutionReady {
		t.Fatalf("status should be unavailable: %+v", status)
	}
	if status.Reason == "" || service.UnavailableReason(t.Context()) == "" {
		t.Fatal("unavailable status must carry a reason")
	}
	if status.Backend != "none" {
		t.Fatalf("backend = %q", status.Backend)
	}
}

func TestGatewayStatusIsExecutionReadyWithoutDeepTutor(t *testing.T) {
	upstream := fakeChatServer(t, []string{"你好"}, nil)
	defer upstream.Close()
	service, _ := newTestService(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = upstream.URL + "/v1/chat/completions"
		cfg.AI.Model = "test-model"
		cfg.AI.APIKey = "secret-key"
	})
	status := service.Status(t.Context())
	if status.Configured {
		t.Fatal("teaching.configured must describe DeepTutor only")
	}
	if !status.ExecutionReady || status.Backend != "gateway" {
		t.Fatalf("gateway should be execution ready: %+v", status)
	}
	if service.UnavailableReason(t.Context()) != "" {
		t.Fatal("gateway-backed teaching should not be unavailable")
	}
}

func TestDeepTutorStatusReachableAndUnreachable(t *testing.T) {
	fake := newFakeDeepTutor(t)
	service, _ := newTestService(t, func(cfg *config.Config) {
		cfg.Teaching.DeepTutorURL = fake.server.URL
		cfg.Teaching.DeepTutorToken = "token"
	})
	status := service.Status(t.Context())
	if !status.Configured || !status.Reachable || status.Version != "fake-1.0" {
		t.Fatalf("deep tutor status: %+v", status)
	}
	if status.BaseURL == "" || strings.Contains(status.BaseURL, "token") {
		t.Fatalf("base url leaked or missing: %q", status.BaseURL)
	}

	downService, _ := newTestService(t, func(cfg *config.Config) {
		cfg.Teaching.DeepTutorURL = "http://127.0.0.1:1"
	})
	down := downService.Status(t.Context())
	if !down.Configured || down.Reachable || down.ExecutionReady || down.Reason == "" {
		t.Fatalf("unreachable deep tutor status: %+v", down)
	}
}

// ---------------------------------------------------------------- prompt rules

func TestHintPromptNeverLeaksAnswer(t *testing.T) {
	var capturedSystem string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		for _, message := range payload.Messages {
			if message.Role == "system" {
				capturedSystem = message.Content
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"提示：先回忆定义。\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	service, db := newTestService(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = upstream.URL
		cfg.AI.Model = "test-model"
	})
	userID := seedUser(t, db, "hint@example.com")
	const analysisMarker = "解析标记：正确答案是B，因为这样才能避免死锁。"
	questionID := seedQuestion(t, db, "以下哪个选项正确？", "B", analysisMarker)
	seedAttempt(t, db, userID, questionID, "B") // even after an attempt, hint must not leak

	request, err := service.PrepareTurn(t.Context(), teaching.PrepareInput{
		UserID: userID, TaskID: "task-1", Kind: "hint",
		Context: map[string]any{"questionId": questionID, "hint": true},
		Prompt:  "给我一个提示，别告诉我答案",
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(request.Sources) == 0 {
		t.Fatal("question source missing")
	}
	for _, source := range request.Sources {
		if strings.Contains(source.Text, analysisMarker) || strings.Contains(source.Text, "参考答案") {
			t.Fatalf("hint source leaked the answer: %s", source.Text)
		}
	}
	request.Identity = teaching.Identity{TeachingUserID: "rka-test"}
	if _, _, err := service.RunTurn(t.Context(), request, nil, func(teaching.Event) error { return nil }); err != nil {
		t.Fatalf("run hint turn: %v", err)
	}
	if !strings.Contains(capturedSystem, "提示模式") || !strings.Contains(capturedSystem, "不透露答案") {
		t.Fatalf("hint instruction missing from system prompt: %s", capturedSystem)
	}
	if strings.Contains(capturedSystem, analysisMarker) || strings.Contains(capturedSystem, "参考答案：") {
		t.Fatalf("hint system prompt leaked the answer: %s", capturedSystem)
	}

	// An explanation after an attempt may include the reference answer.
	afterAttempt, err := service.PrepareTurn(t.Context(), teaching.PrepareInput{
		UserID: userID, Kind: "explain",
		Context: map[string]any{"questionId": questionID},
		Prompt:  "我选了B，为什么？",
	})
	if err != nil {
		t.Fatalf("prepare explain: %v", err)
	}
	combined := ""
	for _, source := range afterAttempt.Sources {
		combined += source.Text
	}
	if !strings.Contains(combined, "参考答案：B") || !strings.Contains(combined, analysisMarker) {
		t.Fatalf("explanation source missing solution after attempt: %s", combined)
	}
}

func TestExplainPromptOmitsSolutionBeforeUserAnswers(t *testing.T) {
	service, db := newTestService(t, nil)
	userID := seedUser(t, db, "before@example.com")
	const analysisMarker = "解析标记：先计算再判断。"
	questionID := seedQuestion(t, db, "以下哪个选项正确？", "B", analysisMarker)
	request, err := service.PrepareTurn(t.Context(), teaching.PrepareInput{
		UserID: userID, Kind: "explain",
		Context: map[string]any{"questionId": questionID},
		Prompt:  "为什么？",
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	for _, source := range request.Sources {
		if strings.Contains(source.Text, analysisMarker) || strings.Contains(source.Text, "参考答案") {
			t.Fatalf("answer leaked before the user answered: %s", source.Text)
		}
	}
}

// A formal exam must keep the reference answer out of AI prompts even when the
// learner answered the same question in an earlier practice session. Otherwise
// an explain task would reveal correctness before the paper is submitted.
func TestExplainPromptHidesSolutionDuringActiveExam(t *testing.T) {
	service, db := newTestService(t, nil)
	userID := seedUser(t, db, "exam@example.com")
	const analysisMarker = "解析标记：正式考试期间不得出现的解析。"
	questionID := seedQuestion(t, db, "以下哪个选项正确？", "B", analysisMarker)
	seedAttempt(t, db, userID, questionID, "B")

	var sessionID string
	if err := db.Pool.QueryRow(t.Context(), `
		insert into practice_sessions (user_id, mode, question_ids, revisions)
		values ($1::uuid, 'exam', array[$2], jsonb_build_object($2, 'r1'))
		returning id::text`, userID, questionID).Scan(&sessionID); err != nil {
		t.Fatalf("seed exam session: %v", err)
	}

	prepare := func() string {
		t.Helper()
		request, err := service.PrepareTurn(t.Context(), teaching.PrepareInput{
			UserID: userID, Kind: "explain",
			Context: map[string]any{"questionId": questionID},
			Prompt:  "我选了B，为什么？",
		})
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		combined := ""
		for _, source := range request.Sources {
			combined += source.Text
		}
		return combined
	}

	duringExam := prepare()
	if strings.Contains(duringExam, analysisMarker) || strings.Contains(duringExam, "参考答案") {
		t.Fatalf("answer leaked during an active exam: %s", duringExam)
	}

	if _, err := db.Pool.Exec(t.Context(), `
		update practice_sessions set status = 'finished', submitted_at = now() where id = $1::uuid`,
		sessionID); err != nil {
		t.Fatalf("submit exam session: %v", err)
	}
	afterExam := prepare()
	if !strings.Contains(afterExam, "参考答案：B") || !strings.Contains(afterExam, analysisMarker) {
		t.Fatalf("answer missing after submission: %s", afterExam)
	}
}

// ---------------------------------------------------------------- gateway adapter

func TestGatewayStreamsDeltasCitationsAndDone(t *testing.T) {
	var gotAuth, gotModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var payload struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		gotModel = payload.Model
		if len(payload.Messages) == 0 {
			t.Error("gateway request has no messages")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		chunks := []string{"先看", "定义，再参考 [S0]", " 的说明。"}
		for _, chunk := range chunks {
			body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": chunk}}}})
			fmt.Fprintf(w, "data: %s\n\n", body)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	service, _ := newTestService(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = upstream.URL
		cfg.AI.Model = "test-model"
		cfg.AI.APIKey = "secret-key"
		cfg.AI.RequestTimeout = 5 * time.Second
	})
	request := teaching.TurnRequest{
		Identity: teaching.Identity{UserID: "u1", TeachingUserID: "rka-u1", Credential: "cred"},
		TaskID:   "task-1", Kind: "explain", Prompt: "解释一下",
		Sources: []teaching.Source{{ID: "S0", Title: "资料一", Text: "定义……", MaterialID: "m1", SectionID: "sec-1"}},
	}
	var events []teaching.Event
	ref, result, err := service.RunTurn(t.Context(), request, nil, func(event teaching.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if gotAuth != "Bearer secret-key" || gotModel != "test-model" {
		t.Fatalf("upstream saw auth=%q model=%q", gotAuth, gotModel)
	}
	if ref.TurnID == "" {
		t.Fatal("gateway turn id missing")
	}
	if result.FinalText != "先看定义，再参考 [S0] 的说明。" {
		t.Fatalf("final text = %q", result.FinalText)
	}
	var deltaText strings.Builder
	var citationCount int
	for _, event := range events {
		switch event.Type {
		case "delta":
			deltaText.WriteString(event.Payload["text"].(string))
		case "citation":
			citationCount++
			if event.Payload["sourceId"] != "S0" || event.Payload["materialId"] != "m1" {
				t.Fatalf("citation payload = %v", event.Payload)
			}
		}
	}
	if deltaText.String() != result.FinalText {
		t.Fatalf("delta text = %q", deltaText.String())
	}
	if citationCount != 1 {
		t.Fatalf("citation events = %d", citationCount)
	}
}

func TestGatewayRejectsFabricatedCitations(t *testing.T) {
	upstream := fakeChatServer(t, []string{"参考 [S9] 和 [S0]。"}, nil)
	defer upstream.Close()
	service, _ := newTestService(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = upstream.URL
		cfg.AI.Model = "test-model"
	})
	request := teaching.TurnRequest{
		Kind: "explain", Prompt: "解释",
		Sources: []teaching.Source{{ID: "S0", Title: "资料一", Text: "内容"}},
	}
	var citations []map[string]any
	_, _, err := service.RunTurn(t.Context(), request, nil, func(event teaching.Event) error {
		if event.Type == "citation" {
			citations = append(citations, event.Payload)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if len(citations) != 1 || citations[0]["sourceId"] != "S0" {
		t.Fatalf("fabricated citation was persisted: %v", citations)
	}
}

func TestGatewayTimeoutIsUnknownState(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"半句\"}}]}\n\n")
		flusher.Flush()
		time.Sleep(400 * time.Millisecond)
	}))
	defer upstream.Close()
	service, _ := newTestService(t, func(cfg *config.Config) {
		cfg.AI.Endpoint = upstream.URL
		cfg.AI.Model = "test-model"
		cfg.AI.RequestTimeout = 120 * time.Millisecond
	})
	_, _, err := service.RunTurn(t.Context(), teaching.TurnRequest{Kind: "explain", Prompt: "解释"}, nil, func(teaching.Event) error { return nil })
	if err == nil || !teaching.IsUnknownState(err) {
		t.Fatalf("expected unknown upstream state, got %v", err)
	}
}

// ---------------------------------------------------------------- deeptutor adapter

type fakeDeepTutor struct {
	server    *httptest.Server
	mu        sync.Mutex
	replies   []string
	cancelled bool
	events    []string
}

func newFakeDeepTutor(t *testing.T) *fakeDeepTutor {
	t.Helper()
	fake := &fakeDeepTutor{events: []string{
		"event: delta\ndata: {\"text\":\"你好，\"}\n\n",
		"event: delta\ndata: {\"text\":\"这是讲解 [S0]。\"}\n\n",
		"event: citation\ndata: {\"sourceId\":\"S0\",\"materialId\":\"m1\",\"sectionId\":\"sec-1\",\"title\":\"资料一\"}\n\n",
		"event: done\ndata: {\"status\":\"completed\"}\n\n",
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok","version":"fake-1.0"}`)
	})
	mux.HandleFunc("POST /api/users", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"tu-1"}`)
	})
	mux.HandleFunc("POST /api/turns", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "rka-") {
			t.Errorf("turn payload missing teaching user: %s", body)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"turnId":"turn-1","sessionId":"session-1"}`)
	})
	mux.HandleFunc("GET /api/turns/turn-1/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fake.mu.Lock()
		events := append([]string(nil), fake.events...)
		fake.mu.Unlock()
		for _, event := range events {
			_, _ = io.WriteString(w, event)
			flusher.Flush()
		}
	})
	mux.HandleFunc("POST /api/turns/turn-1/messages", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Content string `json:"content"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		fake.mu.Lock()
		fake.replies = append(fake.replies, payload.Content)
		fake.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /api/turns/turn-1/cancel", func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.cancelled = true
		fake.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /api/turns/turn-1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"completed","output":"恢复的完整文本","sessionId":"session-1"}`)
	})
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func TestDeepTutorTurnStreamingReplyAndRecovery(t *testing.T) {
	fake := newFakeDeepTutor(t)
	service, _ := newTestService(t, func(cfg *config.Config) {
		cfg.Teaching.DeepTutorURL = fake.server.URL
		cfg.Teaching.DeepTutorToken = "dt-token"
	})
	request := teaching.TurnRequest{
		Identity: teaching.Identity{UserID: "u1", TeachingUserID: "rka-u1", Credential: "cred"},
		TaskID:   "task-1", Kind: "explain", Prompt: "解释",
		Sources: []teaching.Source{{ID: "S0", Title: "资料一", Text: "内容", MaterialID: "m1", SectionID: "sec-1"}},
	}
	var events []teaching.Event
	ref, result, err := service.RunTurn(t.Context(), request, nil, func(event teaching.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if ref.TurnID != "turn-1" || ref.SessionID != "session-1" {
		t.Fatalf("turn ref = %+v", ref)
	}
	if result.FinalText != "你好，这是讲解 [S0]。" {
		t.Fatalf("final text = %q", result.FinalText)
	}
	var types []string
	for _, event := range events {
		types = append(types, event.Type)
	}
	if strings.Join(types, ",") != "delta,delta,citation,done" {
		t.Fatalf("event order = %v", types)
	}

	// A follow-up continues the existing DeepTutor turn through /messages.
	request.ResumeReply = "再展开一点"
	if _, _, err := service.RunTurn(t.Context(), request, &ref, func(teaching.Event) error { return nil }); err != nil {
		t.Fatalf("resume turn: %v", err)
	}
	fake.mu.Lock()
	replies := append([]string(nil), fake.replies...)
	fake.mu.Unlock()
	if len(replies) != 1 || replies[0] != "再展开一点" {
		t.Fatalf("deeptutor replies = %v", replies)
	}

	recovery, err := service.Recover(t.Context(), ref)
	if err != nil || recovery.State != teaching.RecoveryCompleted || recovery.Result.FinalText != "恢复的完整文本" {
		t.Fatalf("recovery = %+v, %v", recovery, err)
	}
	if err := service.Cancel(t.Context(), ref); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	fake.mu.Lock()
	cancelled := fake.cancelled
	fake.mu.Unlock()
	if !cancelled {
		t.Fatal("deeptutor cancel endpoint was not called")
	}
}

func TestDeepTutorQuestionWaitsForUser(t *testing.T) {
	fake := newFakeDeepTutor(t)
	fake.mu.Lock()
	fake.events = []string{
		"event: delta\ndata: {\"text\":\"先思考：\"}\n\n",
		"event: question\ndata: {\"text\":\"你认为哪个选项最接近？\"}\n\n",
	}
	fake.mu.Unlock()
	service, _ := newTestService(t, func(cfg *config.Config) {
		cfg.Teaching.DeepTutorURL = fake.server.URL
		cfg.Teaching.DeepTutorToken = "legacy-test-token"
	})
	request := teaching.TurnRequest{Identity: teaching.Identity{TeachingUserID: "rka-u1"}, Kind: "hint", Prompt: "给个提示"}
	var sawQuestion bool
	_, result, err := service.RunTurn(t.Context(), request, nil, func(event teaching.Event) error {
		if event.Type == "question" {
			sawQuestion = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if !sawQuestion || !result.Waiting {
		t.Fatalf("question result = %+v, saw=%v", result, sawQuestion)
	}
}

// ---------------------------------------------------------------- fakes

func fakeChatServer(t *testing.T, chunks []string, headers map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for key, value := range headers {
			w.Header().Set(key, value)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test server does not support flushing")
			return
		}
		for _, chunk := range chunks {
			body, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": chunk}}}})
			fmt.Fprintf(w, "data: %s\n\n", body)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	return server
}

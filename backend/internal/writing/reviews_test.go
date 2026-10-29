package writing_test

import (
	"net/http"
	"testing"

	"rka/internal/httpx"
	"rka/internal/testutil"
)

func TestReviewBindsDocumentVersionAndWarnsWhenStale(t *testing.T) {
	env := newTestEnv(t)
	insertSubjectiveItem(t, env.db, "case-1", "case")
	env.createUser("writer@example.com")
	env.login(env.server, "writer@example.com")

	status, body := env.putDocument(env.server, "case", "case-1", map[string]string{
		"answer-0": "第一版答案",
	}, 0)
	testutil.MustStatus(t, http.StatusOK, status, body)
	documentID := stringValue(documentOf(t, body), "id")

	env.ai.id = newUUID()
	status, body = env.server.Do(t, http.MethodPost, "/api/writing/case/case-1/reviews", map[string]any{})
	testutil.MustStatus(t, http.StatusCreated, status, body)
	review := reviewOf(t, body)
	if got := numberValue(t, review, "documentVersion"); got != 1 {
		t.Fatalf("review documentVersion = %d, want 1", got)
	}
	if stringValue(review, "taskId") != env.ai.id {
		t.Fatalf("review taskId = %q, want %q", stringValue(review, "taskId"), env.ai.id)
	}
	if stringValue(review, "status") != "queued" {
		t.Fatalf("review status = %q, want queued", stringValue(review, "status"))
	}
	if review["stale"] != false {
		t.Fatalf("fresh review stale = %v, want false", review["stale"])
	}

	if env.ai.callCount() != 1 {
		t.Fatalf("task calls = %d, want 1", env.ai.callCount())
	}
	call := env.ai.call(0)
	if call.Kind != "review_case" {
		t.Fatalf("task kind = %q, want review_case", call.Kind)
	}
	wantKey := "writing-review:" + documentID + ":1"
	if call.IdempotencyKey != wantKey {
		t.Fatalf("idempotency key = %q, want %q", call.IdempotencyKey, wantKey)
	}
	if call.Context["documentId"] != documentID {
		t.Fatalf("context documentId = %v", call.Context["documentId"])
	}
	if call.Context["documentVersion"] != 1 {
		t.Fatalf("context documentVersion = %v", call.Context["documentVersion"])
	}
	if call.Context["kind"] != "case" || call.Context["targetId"] != "case-1" {
		t.Fatalf("context kind/targetId = %v / %v", call.Context["kind"], call.Context["targetId"])
	}
	summary, ok := call.Context["values"].(map[string]any)
	if !ok {
		t.Fatalf("context values summary = %T, want object", call.Context["values"])
	}
	if summary["totalChars"] == 0 {
		t.Fatalf("context values summary has no character count: %v", summary)
	}

	// Saving a new version makes the earlier review stale: adopting it must be
	// presented as a version change, not applied silently.
	status, body = env.putDocument(env.server, "case", "case-1", map[string]string{
		"answer-0": "第二版答案",
	}, 1)
	testutil.MustStatus(t, http.StatusOK, status, body)

	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-1/reviews", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if got := numberValue(t, body, "currentDocumentVersion"); got != 2 {
		t.Fatalf("currentDocumentVersion = %d, want 2", got)
	}
	items := arrayValue(t, body["items"], "items")
	if len(items) != 1 {
		t.Fatalf("review count = %d, want 1", len(items))
	}
	staleReview := objectValue(t, items[0], "items[0]")
	if staleReview["stale"] != true {
		t.Fatalf("review bound to version 1 should be stale: %v", staleReview)
	}
	if numberValue(t, staleReview, "documentVersion") != 1 {
		t.Fatalf("stale review version = %v", staleReview)
	}

	// Explicitly asking to review an outdated version is rejected with the
	// server version so the client can warn before adopting suggestions.
	status, body = env.server.Do(t, http.MethodPost, "/api/writing/case/case-1/reviews", map[string]any{"version": 1})
	testutil.MustStatus(t, http.StatusConflict, status, body)
	if code := errorCode(t, body); code != "stale_version" {
		t.Fatalf("error code = %q, want stale_version", code)
	}
	if got := numberValue(t, detailsOf(t, body), "serverVersion"); got != 2 {
		t.Fatalf("stale_version serverVersion = %d, want 2", got)
	}

	// Reviewing the current version creates a new review bound to it.
	env.ai.id = newUUID()
	status, body = env.server.Do(t, http.MethodPost, "/api/writing/case/case-1/reviews", map[string]any{})
	testutil.MustStatus(t, http.StatusCreated, status, body)
	currentReview := reviewOf(t, body)
	if numberValue(t, currentReview, "documentVersion") != 2 {
		t.Fatalf("current review = %v", currentReview)
	}
	if env.ai.call(1).IdempotencyKey != "writing-review:"+documentID+":2" {
		t.Fatalf("second idempotency key = %q", env.ai.call(1).IdempotencyKey)
	}
	if env.ai.callCount() != 2 {
		t.Fatalf("task calls = %d, want 2", env.ai.callCount())
	}

	// Repeating the same request is idempotent on document id + version.
	status, body = env.server.Do(t, http.MethodPost, "/api/writing/case/case-1/reviews", map[string]any{})
	testutil.MustStatus(t, http.StatusOK, status, body)
	if stringValue(reviewOf(t, body), "id") != stringValue(currentReview, "id") {
		t.Fatalf("repeat review returned a different row: %v", body)
	}
	if env.ai.callCount() != 2 {
		t.Fatalf("repeat created another task: %d calls", env.ai.callCount())
	}
}

func TestEssayReviewUsesEssayTaskKind(t *testing.T) {
	env := newTestEnv(t)
	insertSubjectiveItem(t, env.db, "essay-1", "essay")
	env.createUser("writer@example.com")
	env.login(env.server, "writer@example.com")

	status, body := env.putDocument(env.server, "essay", "essay-1", map[string]string{
		"material": "项目素材",
		"outline":  "提纲",
		"abstract": "摘要",
		"body":     "正文",
	}, 0)
	testutil.MustStatus(t, http.StatusOK, status, body)

	status, body = env.server.Do(t, http.MethodPost, "/api/writing/essay/essay-1/reviews", map[string]any{})
	testutil.MustStatus(t, http.StatusCreated, status, body)
	if got := reviewOf(t, body)["status"]; got != "queued" {
		t.Fatalf("review status = %v, want queued", got)
	}
	if env.ai.call(0).Kind != "review_essay" {
		t.Fatalf("task kind = %q, want review_essay", env.ai.call(0).Kind)
	}
}

func TestReviewStatusRefreshesFromAITask(t *testing.T) {
	env := newTestEnv(t)
	insertSubjectiveItem(t, env.db, "case-1", "case")
	user := env.createUser("writer@example.com")
	env.login(env.server, "writer@example.com")

	status, body := env.putDocument(env.server, "case", "case-1", map[string]string{
		"answer-0": "第一版答案",
	}, 0)
	testutil.MustStatus(t, http.StatusOK, status, body)

	env.ai.id = newUUID()
	status, body = env.server.Do(t, http.MethodPost, "/api/writing/case/case-1/reviews", map[string]any{})
	testutil.MustStatus(t, http.StatusCreated, status, body)
	reviewID := stringValue(reviewOf(t, body), "id")
	taskID := env.ai.id

	if _, err := env.db.Pool.Exec(t.Context(), `
		insert into ai_tasks (id, user_id, kind, status, idempotency_key)
		values ($1::uuid, $2::uuid, 'review_case', 'running', 'manual-review-1')`,
		taskID, user.ID); err != nil {
		t.Fatalf("insert ai task: %v", err)
	}

	// Reading the review history refreshes queued -> running.
	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-1/reviews", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	review := objectValue(t, arrayValue(t, body["items"], "items")[0], "items[0]")
	if stringValue(review, "status") != "running" {
		t.Fatalf("review status = %q, want running", stringValue(review, "status"))
	}

	// A finished task writes the feedback back into the review projection.
	if _, err := env.db.Pool.Exec(t.Context(),
		`update ai_tasks set status = 'completed', finished_at = now() where id = $1::uuid`, taskID); err != nil {
		t.Fatalf("complete ai task: %v", err)
	}
	if _, err := env.db.Pool.Exec(t.Context(), `
		insert into ai_events (task_id, seq, type, payload)
		values ($1::uuid, 1, 'done', '{"feedback":{"summary":"结构清晰，但缺少可证实的效果指标"}}'::jsonb)`,
		taskID); err != nil {
		t.Fatalf("insert done event: %v", err)
	}

	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-1/reviews", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	review = objectValue(t, arrayValue(t, body["items"], "items")[0], "items[0]")
	if stringValue(review, "status") != "completed" {
		t.Fatalf("review status = %q, want completed", stringValue(review, "status"))
	}
	feedback := objectValue(t, review["feedback"], "feedback")
	if feedback["summary"] != "结构清晰，但缺少可证实的效果指标" {
		t.Fatalf("feedback = %v", feedback)
	}
	if review["completedAt"] == nil {
		t.Fatalf("completed review has no completedAt: %v", review)
	}

	var storedStatus string
	if err := env.db.Pool.QueryRow(t.Context(),
		`select status from writing_reviews where id = $1::uuid`, reviewID).Scan(&storedStatus); err != nil {
		t.Fatalf("read stored review: %v", err)
	}
	if storedStatus != "completed" {
		t.Fatalf("stored review status = %q, want completed", storedStatus)
	}

	// Failures are surfaced with their reason instead of staying queued.
	status, body = env.putDocument(env.server, "case", "case-1", map[string]string{
		"answer-0": "第二版答案",
	}, 1)
	testutil.MustStatus(t, http.StatusOK, status, body)
	env.ai.id = newUUID()
	status, body = env.server.Do(t, http.MethodPost, "/api/writing/case/case-1/reviews", map[string]any{})
	testutil.MustStatus(t, http.StatusCreated, status, body)
	failedTask := env.ai.id
	if _, err := env.db.Pool.Exec(t.Context(), `
		insert into ai_tasks (id, user_id, kind, status, idempotency_key, error_code, error_message)
		values ($1::uuid, $2::uuid, 'review_case', 'failed', 'manual-review-2', 'upstream_error', '模型服务返回失败')`,
		failedTask, user.ID); err != nil {
		t.Fatalf("insert failed ai task: %v", err)
	}

	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-1/reviews", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	failed := objectValue(t, arrayValue(t, body["items"], "items")[0], "items[0]")
	if stringValue(failed, "status") != "failed" {
		t.Fatalf("failed review status = %q, want failed", stringValue(failed, "status"))
	}
	failure := objectValue(t, objectValue(t, failed["feedback"], "feedback")["error"], "feedback.error")
	if failure["message"] != "模型服务返回失败" {
		t.Fatalf("failure feedback = %v", failure)
	}
}

func TestReviewCreationReportsAIUnavailable(t *testing.T) {
	env := newTestEnv(t)
	insertSubjectiveItem(t, env.db, "case-1", "case")
	env.createUser("writer@example.com")
	env.login(env.server, "writer@example.com")

	status, body := env.putDocument(env.server, "case", "case-1", map[string]string{
		"answer-0": "第一版答案",
	}, 0)
	testutil.MustStatus(t, http.StatusOK, status, body)

	env.ai.err = httpx.NewError(http.StatusServiceUnavailable, "ai_unavailable", "模型服务尚未配置，暂时无法发起点评。")
	status, body = env.server.Do(t, http.MethodPost, "/api/writing/case/case-1/reviews", map[string]any{})
	testutil.MustStatus(t, http.StatusServiceUnavailable, status, body)
	if code := errorCode(t, body); code != "ai_unavailable" {
		t.Fatalf("error code = %q, want ai_unavailable", code)
	}

	var reviews int
	if err := env.db.Pool.QueryRow(t.Context(), `select count(*) from writing_reviews`).Scan(&reviews); err != nil {
		t.Fatalf("count reviews: %v", err)
	}
	if reviews != 0 {
		t.Fatalf("failed task creation left %d review rows", reviews)
	}

	// Once the gateway is available the same request succeeds.
	env.ai.err = nil
	status, body = env.server.Do(t, http.MethodPost, "/api/writing/case/case-1/reviews", map[string]any{})
	testutil.MustStatus(t, http.StatusCreated, status, body)
}

package content_test

import (
	"net/http"
	"testing"

	"rka/internal/testutil"
)

func TestAdminImportEndpointAndJobs(t *testing.T) {
	env := newTestEnv(t)
	dir := writeFixture(t, "test-v1", false)

	status, body := env.learner.Do(t, http.MethodPost, "/api/admin/content/import", map[string]any{"snapshotDir": dir})
	if status != http.StatusForbidden {
		t.Fatalf("learner import status = %d body=%v", status, body)
	}

	status, body = env.admin.Do(t, http.MethodPost, "/api/admin/content/import", map[string]any{"snapshotDir": dir})
	testutil.MustStatus(t, http.StatusOK, status, body)
	job, _ := body["job"].(map[string]any)
	if job["status"] != "completed" {
		t.Errorf("job status = %v, want completed", job["status"])
	}
	stats, _ := job["stats"].(map[string]any)
	if stats["version"] != "test-v1" || stats["materials"] != float64(2) {
		t.Errorf("job stats = %v", stats)
	}

	status, body = env.admin.Do(t, http.MethodGet, "/api/admin/content/jobs", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("jobs = %d, want 1", len(items))
	}
	jobID, _ := job["id"].(string)
	status, body = env.admin.Do(t, http.MethodGet, "/api/admin/content/jobs/"+jobID, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["job"].(map[string]any)["id"] != jobID {
		t.Errorf("single job = %v", body["job"])
	}
	// Importing the same directory through the endpoint is idempotent.
	status, body = env.admin.Do(t, http.MethodPost, "/api/admin/content/import", map[string]any{"snapshotDir": dir})
	testutil.MustStatus(t, http.StatusOK, status, body)
	result, _ := body["result"].(map[string]any)
	if result["skipped"] != true {
		t.Errorf("second import result = %v, want skipped", result)
	}
}

func TestAdminLinkReviewFiltersCatalog(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.app.Content.ImportSnapshot(t.Context(), writeFixture(t, "test-v1", false), ""); err != nil {
		t.Fatalf("import fixture: %v", err)
	}
	status, body := env.learner.Do(t, http.MethodGet, "/api/content/links?questionId=q-1", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("links = %d, want 1", len(items))
	}
	link, _ := items[0].(map[string]any)
	linkID, _ := link["id"].(string)
	if link["status"] != "auto" || link["sectionKey"] == "" {
		t.Errorf("link = %v", link)
	}

	status, body = env.admin.Do(t, http.MethodPatch, "/api/admin/content/links/"+linkID, map[string]any{"status": "approved"})
	testutil.MustStatus(t, http.StatusOK, status, body)
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/links?questionId=q-1", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items, _ = body["items"].([]any)
	link, _ = items[0].(map[string]any)
	if link["status"] != "approved" {
		t.Errorf("link status after approval = %v", link["status"])
	}

	status, body = env.admin.Do(t, http.MethodPatch, "/api/admin/content/links/"+linkID, map[string]any{"status": "rejected"})
	testutil.MustStatus(t, http.StatusOK, status, body)
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/links?questionId=q-1", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if items, _ := body["items"].([]any); len(items) != 0 {
		t.Errorf("rejected link still visible: %v", items)
	}
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/catalog", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if links, _ := body["knowledgeLinks"].(map[string]any); len(links) != 0 {
		t.Errorf("catalog still exposes rejected link: %v", links)
	}

	status, body = env.admin.Do(t, http.MethodPatch, "/api/admin/content/links/"+linkID, map[string]any{"status": "unsure"})
	if status != http.StatusBadRequest {
		t.Errorf("bad link status = %d body=%v", status, body)
	}
}

func TestAdminQuestionRevisionKeepsOldAttempts(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.app.Content.ImportSnapshot(t.Context(), writeFixture(t, "test-v1", false), ""); err != nil {
		t.Fatalf("import fixture: %v", err)
	}
	insertAttempt(t, env.db, env.learnerID, "q-1", "rev-1", "B", true)

	status, body := env.admin.Do(t, http.MethodPatch, "/api/admin/content/questions/q-1", map[string]any{
		"answer":   "C",
		"analysis": "修订后的解析：范式化的目标是减少冗余。",
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["created"] != true {
		t.Errorf("created = %v", body["created"])
	}
	newRevision, _ := body["revision"].(string)
	if newRevision == "" || newRevision == "rev-1" {
		t.Fatalf("new revision = %q", newRevision)
	}
	if got := scalarInt(t, env.db, `select count(*) from question_versions where question_id = 'q-1'`); got != 2 {
		t.Errorf("question versions = %d, want 2", got)
	}
	if got := scalarString(t, env.db,
		`select question_revision from attempts where user_id = $1::uuid and question_id = 'q-1'`, env.learnerID); got != "rev-1" {
		t.Errorf("attempt revision changed to %q", got)
	}
	status, body = env.admin.Do(t, http.MethodGet, "/api/content/questions/q-1/solution", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["answer"] != "C" || body["revision"] != newRevision {
		t.Errorf("new solution = %v", body)
	}

	// Repeating the same patch is idempotent: no third version is created.
	status, body = env.admin.Do(t, http.MethodPatch, "/api/admin/content/questions/q-1", map[string]any{
		"answer":   "C",
		"analysis": "修订后的解析：范式化的目标是减少冗余。",
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["created"] != false {
		t.Errorf("second patch created = %v", body["created"])
	}
	if got := scalarInt(t, env.db, `select count(*) from question_versions where question_id = 'q-1'`); got != 2 {
		t.Errorf("question versions after repeat = %d, want 2", got)
	}

	// Invalid answer is rejected without creating a revision.
	status, body = env.admin.Do(t, http.MethodPatch, "/api/admin/content/questions/q-1", map[string]any{"answer": "Z"})
	if status != http.StatusBadRequest {
		t.Errorf("bad answer status = %d body=%v", status, body)
	}
}

func TestAdminReviewStatusesGateLearnerAccess(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.app.Content.ImportSnapshot(t.Context(), writeFixture(t, "test-v1", false), ""); err != nil {
		t.Fatalf("import fixture: %v", err)
	}
	status, body := env.admin.Do(t, http.MethodPatch, "/api/admin/content/questions/q-1/review", map[string]any{"reviewStatus": "needs_review"})
	testutil.MustStatus(t, http.StatusOK, status, body)
	question, _ := body["question"].(map[string]any)
	if question["reviewStatus"] != "needs_review" {
		t.Errorf("question reviewStatus = %v", question["reviewStatus"])
	}

	status, body = env.admin.Do(t, http.MethodPatch, "/api/admin/content/materials/material-a/review", map[string]any{"reviewStatus": "draft"})
	testutil.MustStatus(t, http.StatusOK, status, body)
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/materials/material-a", nil)
	if status != http.StatusForbidden {
		t.Errorf("draft material learner status = %d body=%v", status, body)
	}
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/catalog", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	materials, _ := body["materials"].([]any)
	if len(materials) != 1 {
		t.Errorf("learner catalog materials = %d, want 1 after draft", len(materials))
	}
	status, body = env.admin.Do(t, http.MethodGet, "/api/content/catalog", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	materials, _ = body["materials"].([]any)
	if len(materials) != 2 {
		t.Errorf("admin catalog materials = %d, want 2", len(materials))
	}
	status, body = env.admin.Do(t, http.MethodPatch, "/api/admin/content/materials/material-a/review", map[string]any{"reviewStatus": "published"})
	testutil.MustStatus(t, http.StatusOK, status, body)
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/materials/material-a", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
}

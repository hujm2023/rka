package ops_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rka/internal/config"
	"rka/internal/store"
	"rka/internal/testutil"
)

const password = "correct-horse-battery"

func errorCode(body map[string]any) string {
	envelope, _ := body["error"].(map[string]any)
	code, _ := envelope["code"].(string)
	return code
}

func insertQuestion(t *testing.T, db *store.DB, id, revision, answer string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Pool.Exec(ctx, `insert into questions (id, source_type) values ($1, 'mock')`, id); err != nil {
		t.Fatalf("insert question %s: %v", id, err)
	}
	var versionID string
	if err := db.Pool.QueryRow(ctx, `
		insert into question_versions (question_id, revision, stem, options, answer)
		values ($1, $2, '题干', '{"A":"a","B":"b","C":"c","D":"d"}'::jsonb, $3)
		returning id::text`, id, revision, answer).Scan(&versionID); err != nil {
		t.Fatalf("insert question version %s: %v", id, err)
	}
	if _, err := db.Pool.Exec(ctx, `update questions set current_version_id = $2::uuid where id = $1`, id, versionID); err != nil {
		t.Fatalf("set current version %s: %v", id, err)
	}
}

func insertMaterial(t *testing.T, db *store.DB, id string) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), `
		insert into materials (id, title, book, kind, module, source_file)
		values ($1, $1, 'book', 'textbook', 'other', $1)`, id); err != nil {
		t.Fatalf("insert material %s: %v", id, err)
	}
}

func insertSubjectiveItem(t *testing.T, db *store.DB, id, kind string) {
	t.Helper()
	if _, err := db.Pool.Exec(context.Background(), `
		insert into subjective_items (id, kind, revision, title, payload)
		values ($1, $2, 'r1', 'title', '{}'::jsonb)`, id, kind); err != nil {
		t.Fatalf("insert subjective item %s: %v", id, err)
	}
}

func TestHealthReportsComponentsSeparately(t *testing.T) {
	application, _, db := testutil.NewApp(t)
	insertMaterial(t, db, "material-1")
	insertQuestion(t, db, "q-health", "rev1", "A")
	if err := application.DB.Pool.QueryRow(t.Context(), `select 1`).Scan(new(int)); err != nil {
		t.Fatalf("db: %v", err)
	}

	report := application.Ops.Health(t.Context())
	if report.Process.Status != "ok" {
		t.Fatalf("process status = %q, want ok", report.Process.Status)
	}
	if report.Database.Status != "ok" {
		t.Fatalf("database status = %q (%s), want ok", report.Database.Status, report.Database.Reason)
	}
	if report.Teaching.Status != "unconfigured" {
		t.Fatalf("teaching status = %q, want unconfigured", report.Teaching.Status)
	}
	if !strings.Contains(report.Teaching.Reason, "RKA_DEEPTUTOR_URL") {
		t.Fatalf("teaching reason should name the missing setting: %q", report.Teaching.Reason)
	}
	if report.Model.Status != "unconfigured" {
		t.Fatalf("model status = %q, want unconfigured", report.Model.Status)
	}
	if report.Index.Status != "unconfigured" {
		t.Fatalf("index status = %q, want unconfigured", report.Index.Status)
	}
	if report.Version.BinarySchema == 0 || report.Version.Schema != report.Version.BinarySchema {
		t.Fatalf("unexpected version info: %+v", report.Version)
	}
}

func TestSyncStateAndImportIdempotent(t *testing.T) {
	application, server, db := testutil.NewApp(t)
	testutil.CreateUser(t, application, "learner@example.com", password, "learner")
	testutil.Login(t, server, "learner@example.com", password)
	insertQuestion(t, db, "q-sync", "rev1", "B")
	insertMaterial(t, db, "m-sync")
	insertSubjectiveItem(t, db, "case-sync", "case")

	status, body := server.Do(t, http.MethodGet, "/api/sync/state", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["serverTime"] == nil || body["bankVersion"] == nil {
		t.Fatalf("sync state missing fields: %v", body)
	}
	userState, _ := body["user"].(map[string]any)
	counts, _ := userState["counts"].(map[string]any)
	if counts["attempts"] != float64(0) {
		t.Fatalf("unexpected initial counts: %v", counts)
	}

	payload := legacyBackupPayload()
	status, body = server.Do(t, http.MethodPost, "/api/sync/import", payload)
	testutil.MustStatus(t, http.StatusOK, status, body)
	imported, _ := body["imported"].(map[string]any)
	if imported["attempts"] != float64(1) || imported["marks"] != float64(1) || imported["drafts"] != float64(1) ||
		imported["reading"] != float64(1) || imported["sessions"] != float64(1) || imported["chats"] != float64(1) {
		t.Fatalf("unexpected imported counts: %v", imported)
	}
	skipped, _ := body["skipped"].([]any)
	if len(skipped) == 0 {
		t.Fatalf("missing question should be reported as skipped: %v", body)
	}
	firstSkip, _ := skipped[0].(map[string]any)
	if !strings.Contains(firstSkip["reason"].(string), "题目不存在") {
		t.Fatalf("skip reason should explain missing question: %v", firstSkip)
	}

	var attemptCount int
	if err := db.Pool.QueryRow(t.Context(), `select count(*) from attempts where question_id = 'q-sync'`).Scan(&attemptCount); err != nil {
		t.Fatal(err)
	}
	if attemptCount != 1 {
		t.Fatalf("attempt count = %d, want 1", attemptCount)
	}

	// Re-importing must not duplicate facts.
	status, body = server.Do(t, http.MethodPost, "/api/sync/import", payload)
	testutil.MustStatus(t, http.StatusOK, status, body)
	imported, _ = body["imported"].(map[string]any)
	if imported["attempts"] != float64(0) || imported["marks"] != float64(0) {
		t.Fatalf("re-import counted duplicates: %v", imported)
	}
	if err := db.Pool.QueryRow(t.Context(), `select count(*) from attempts where question_id = 'q-sync'`).Scan(&attemptCount); err != nil {
		t.Fatal(err)
	}
	if attemptCount != 1 {
		t.Fatalf("re-import duplicated attempt: count=%d", attemptCount)
	}
}

func TestSyncImportRejectsIncompleteBackupWithoutWriting(t *testing.T) {
	application, server, db := testutil.NewApp(t)
	testutil.CreateUser(t, application, "learner@example.com", password, "learner")
	testutil.Login(t, server, "learner@example.com", password)
	insertQuestion(t, db, "q-incomplete", "rev1", "A")

	status, body := server.Do(t, http.MethodPost, "/api/sync/import", map[string]any{
		"app": "rka", "schemaVersion": 1,
		"data": map[string]any{"attempts": []any{}},
	})
	testutil.MustStatus(t, http.StatusBadRequest, status, body)
	if code := errorCode(body); code != "invalid_backup" {
		t.Fatalf("error code = %q, want invalid_backup", code)
	}
	message, _ := body["error"].(map[string]any)["message"].(string)
	if !strings.Contains(message, "数据不完整") {
		t.Fatalf("message should explain incomplete data: %q", message)
	}
	var count int
	_ = db.Pool.QueryRow(t.Context(), `select count(*) from attempts`).Scan(&count)
	if count != 0 {
		t.Fatalf("invalid backup wrote rows: %d", count)
	}
}

func TestExportAndDeleteMyData(t *testing.T) {
	application, server, db := testutil.NewApp(t)
	user := testutil.CreateUser(t, application, "learner@example.com", password, "learner")
	testutil.Login(t, server, "learner@example.com", password)
	insertQuestion(t, db, "q-export", "rev1", "A")
	insertMaterial(t, db, "m-export")
	if _, err := db.Pool.Exec(t.Context(), `
		insert into attempts (id, user_id, question_id, question_revision, selected, correct, answered_at)
		values ('att-export', $1::uuid, 'q-export', 'rev1', 'A', true, now())`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into marks (user_id, question_id, favorite, note) values ($1::uuid, 'q-export', true, '笔记')`, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into reading_progress (user_id, material_id, progress, done) values ($1::uuid, 'm-export', 0.5, false)`, user.ID); err != nil {
		t.Fatal(err)
	}

	status, body := server.Do(t, http.MethodGet, "/api/export/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["app"] != "rka" {
		t.Fatalf("export app = %v", body["app"])
	}
	data, _ := body["data"].(map[string]any)
	if attempts, _ := data["attempts"].([]any); len(attempts) != 1 {
		t.Fatalf("export attempts = %v", data["attempts"])
	}
	exported := body["exportedAt"]
	if exported == nil {
		t.Fatalf("missing exportedAt")
	}
	serialized, _ := json.Marshal(body)
	if strings.Contains(string(serialized), "password") || strings.Contains(string(serialized), "credential") {
		t.Fatalf("export leaked credentials: %s", string(serialized)[:200])
	}

	status, body = server.Do(t, http.MethodDelete, "/api/me/data", map[string]any{"confirm": "delete"})
	testutil.MustStatus(t, http.StatusBadRequest, status, body)
	if code := errorCode(body); code != "confirmation_required" {
		t.Fatalf("error code = %q", code)
	}
	status, body = server.Do(t, http.MethodDelete, "/api/me/data", map[string]any{"confirm": "DELETE"})
	testutil.MustStatus(t, http.StatusOK, status, body)
	deleted, _ := body["deleted"].(map[string]any)
	if deleted["attempts"] != float64(1) || deleted["marks"] != float64(1) {
		t.Fatalf("unexpected deleted counts: %v", deleted)
	}
	var attempts int
	_ = db.Pool.QueryRow(t.Context(), `select count(*) from attempts where user_id = $1::uuid`, user.ID).Scan(&attempts)
	if attempts != 0 {
		t.Fatalf("attempts not deleted: %d", attempts)
	}
	// The account and login session survive.
	status, body = server.Do(t, http.MethodGet, "/api/auth/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["user"] == nil {
		t.Fatalf("account was deleted with learning data")
	}
}

func TestRetentionPolicyAndRun(t *testing.T) {
	cfg := testutil.Config(t)
	cfg.Retention = config.RetentionConfig{TaskDays: 90, EventDays: 30, AuditDays: 365, BackupDays: 30, LogDays: 30}
	application, server, db := testutil.NewAppWithConfig(t, cfg)
	testutil.CreateUser(t, application, "admin@example.com", password, "admin")
	testutil.Login(t, server, "admin@example.com", password)

	status, body := server.Do(t, http.MethodGet, "/api/admin/retention", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	policy, _ := body["policy"].(map[string]any)
	if policy["audit"] != float64(365) {
		t.Fatalf("default policy audit = %v", policy["audit"])
	}

	status, body = server.Do(t, http.MethodPut, "/api/admin/retention", map[string]any{
		"tasks": 1, "events": 30, "audit": 1, "backups": 1, "logs": 1,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	policy, _ = body["policy"].(map[string]any)
	if policy["audit"] != float64(1) || policy["tasks"] != float64(1) {
		t.Fatalf("updated policy = %v", policy)
	}

	// Old audit row must be removed; fresh row must stay.
	if _, err := db.Pool.Exec(t.Context(), `insert into audit_log (action, created_at) values ('old', now() - interval '2 days')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `insert into audit_log (action) values ('fresh')`); err != nil {
		t.Fatal(err)
	}
	// Old completed AI task must be removed with the task policy.
	if _, err := db.Pool.Exec(t.Context(), `
		insert into ai_tasks (user_id, kind, status, idempotency_key, created_at, finished_at)
		select id, 'hint', 'completed', 'old-task', now() - interval '2 days', now() - interval '2 days'
		from users limit 1`); err != nil {
		t.Fatal(err)
	}
	// Backup batch directories: only timestamped batches may be removed.
	oldDir := filepath.Join(cfg.BackupDir(), "20200101T000000Z")
	if err := os.MkdirAll(oldDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into backups (rel_path, status, created_at)
		values ('backups/20200101T000000Z', 'complete', now() - interval '2 days')`); err != nil {
		t.Fatal(err)
	}
	keepDir := filepath.Join(cfg.BackupDir(), "keep-me")
	if err := os.MkdirAll(keepDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// Old log file.
	oldLog := filepath.Join(cfg.LogDir(), "old.log")
	if err := os.WriteFile(oldLog, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(oldLog, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	stats, err := application.Ops.RunRetention(t.Context())
	if err != nil {
		t.Fatalf("run retention: %v", err)
	}
	if stats["audit"] < 1 || stats["tasks"] < 1 || stats["backups"] < 1 || stats["logs"] < 1 {
		t.Fatalf("retention stats = %v", stats)
	}
	var oldAudit int
	_ = db.Pool.QueryRow(t.Context(), `select count(*) from audit_log where action = 'old'`).Scan(&oldAudit)
	if oldAudit != 0 {
		t.Fatalf("old audit row survived")
	}
	var freshAudit int
	_ = db.Pool.QueryRow(t.Context(), `select count(*) from audit_log where action = 'fresh'`).Scan(&freshAudit)
	if freshAudit != 1 {
		t.Fatalf("fresh audit row was removed")
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("old backup directory was not removed")
	}
	if _, err := os.Stat(keepDir); err != nil {
		t.Fatalf("retention removed a non-batch directory: %v", err)
	}
	if _, err := os.Stat(oldLog); !os.IsNotExist(err) {
		t.Fatalf("old log file was not removed")
	}
}

func legacyBackupPayload() map[string]any {
	return map[string]any{
		"app": "rka", "schemaVersion": 1, "exportedAt": "2026-10-01T10:00:00Z",
		"data": map[string]any{
			"attempts": []any{
				map[string]any{"id": "att-1", "questionId": "q-sync", "revision": "rev1", "answer": "B",
					"correct": true, "assisted": false, "createdAt": "2026-10-01T10:00:00Z", "durationMs": 1200},
				map[string]any{"id": "att-missing", "questionId": "q-missing", "revision": "rev1", "answer": "A",
					"correct": false, "assisted": false, "createdAt": "2026-10-01T10:01:00Z", "durationMs": 500},
			},
			"marks": []any{map[string]any{"id": "q-sync", "favorite": true, "uncertain": false, "note": "note"}},
			"sessions": []any{map[string]any{
				"id": "sess-1", "questionIds": []string{"q-sync"}, "index": 0, "answers": map[string]any{},
				"filters": map[string]any{"mode": "all", "module": "all", "term": "all", "source": "all",
					"query": "", "count": 20, "includeMissing": false},
				"elapsed": 10, "updatedAt": "2026-10-01T10:02:00Z", "bankVersion": "v1",
			}},
			"drafts": []any{map[string]any{"id": "case-sync", "kind": "case",
				"values": map[string]string{"answer-0": "我的回答"}, "updatedAt": "2026-10-01T10:03:00Z"}},
			"reading": []any{map[string]any{"id": "m-sync", "progress": 0.5, "done": false,
				"updatedAt": "2026-10-01T10:04:00Z", "sectionId": "s1", "sectionOffset": 10}},
			"chats": []any{map[string]any{"id": "chat-1", "updatedAt": "2026-10-01T10:05:00Z",
				"messages": []any{map[string]any{"id": "msg-1", "role": "user", "content": "你好",
					"createdAt": "2026-10-01T10:05:00Z"}}}},
		},
	}
}

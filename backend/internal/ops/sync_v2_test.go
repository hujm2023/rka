package ops_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"rka/internal/store"
	"rka/internal/testutil"
)

// seedV2AccountData creates representative personal data across every resource
// category that GET /api/export/me emits.
func seedV2AccountData(t *testing.T, db *store.DB, userID, materialID, questionID, knowledgePointID string) {
	t.Helper()
	insertMaterial(t, db, materialID)
	insertQuestion(t, db, questionID, "rev1", "B")
	insertSubjectiveItem(t, db, "case-v2", "case")
	if _, err := db.Pool.Exec(t.Context(), `
		insert into knowledge_points (id, name, module) values ($1, '测试知识点', 'other')`, knowledgePointID); err != nil {
		t.Fatalf("insert knowledge point: %v", err)
	}

	content := []byte("# 测试资料\n")
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	if _, err := db.Pool.Exec(t.Context(), `
		insert into blobs (sha256, size, media_type, rel_path) values ($1, $2, 'text/markdown; charset=utf-8', $3)`,
		sha, len(content), "materials/"+sha+".md"); err != nil {
		t.Fatalf("insert blob: %v", err)
	}
	var materialVersionID string
	if err := db.Pool.QueryRow(t.Context(), `
		insert into material_versions (material_id, version, sha256, chars, index_status)
		values ($1, 1, $2, 10, 'ready') returning id::text`, materialID, sha).Scan(&materialVersionID); err != nil {
		t.Fatalf("insert material version: %v", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		update materials set current_version_id = $2::uuid where id = $1`, materialID, materialVersionID); err != nil {
		t.Fatalf("set material version: %v", err)
	}

	sessionID := "11111111-1111-1111-1111-111111111111"
	if _, err := db.Pool.Exec(t.Context(), `
		insert into practice_sessions
			(id, user_id, mode, scope, question_ids, revisions, index, elapsed_seconds, status, bank_version, version)
		values ($1::uuid, $2::uuid, 'chapter', '{"materialId":"material-v2"}'::jsonb, array[$3], '{"q-v2":"rev1"}'::jsonb, 0, 60, 'finished', 'bank-v1', 1)`,
		sessionID, userID, questionID); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into attempts (id, user_id, session_id, question_id, question_revision, selected, correct, assisted, duration_ms, answered_at)
		values ('att-v2-1', $1::uuid, $2::uuid, $3, 'rev1', 'B', true, false, 1200, now() - interval '3 minutes')`,
		userID, sessionID, questionID); err != nil {
		t.Fatalf("insert attempt: %v", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into marks (user_id, question_id, favorite, uncertain, note, version)
		values ($1::uuid, $2, true, false, '这是笔记', 2)`, userID, questionID); err != nil {
		t.Fatalf("insert mark: %v", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into reading_progress (user_id, material_id, material_version_id, progress, section_anchor, section_offset, done)
		values ($1::uuid, $2, $3::uuid, 0.5, 'anchor-1', 10, false)`, userID, materialID, materialVersionID); err != nil {
		t.Fatalf("insert reading: %v", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into annotations (user_id, material_id, material_version_id, section_anchor, quote, note)
		values ($1::uuid, $2, $3::uuid, 'anchor-1', '摘录', '批注')`, userID, materialID, materialVersionID); err != nil {
		t.Fatalf("insert annotation: %v", err)
	}

	documentID := "22222222-2222-2222-2222-222222222222"
	if _, err := db.Pool.Exec(t.Context(), `
		insert into writing_documents (id, user_id, kind, target_id, values, version)
		values ($1::uuid, $2::uuid, 'case', 'case-v2', '{"answer-0":"我的回答"}'::jsonb, 2)`,
		documentID, userID); err != nil {
		t.Fatalf("insert writing document: %v", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into writing_versions (id, document_id, version, values, source, created_by) values
		('33333333-3333-3333-3333-333333333333', $1::uuid, 1, '{"answer-0":"第一版"}'::jsonb, 'user', $2::uuid),
		('44444444-4444-4444-4444-444444444444', $1::uuid, 2, '{"answer-0":"我的回答"}'::jsonb, 'user', $2::uuid)`,
		documentID, userID); err != nil {
		t.Fatalf("insert writing versions: %v", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into writing_reviews (id, document_id, document_version, status, feedback)
		values ('55555555-5555-5555-5555-555555555555', $1::uuid, 2, 'completed', '{"summary":"不错"}'::jsonb)`,
		documentID); err != nil {
		t.Fatalf("insert writing review: %v", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into evidence (id, user_id, kind, question_id, knowledge_point_id, material_id, payload, occurred_at)
		values ('66666666-6666-6666-6666-666666666666', $1::uuid, 'independent_pass', $2, $3, $4, '{}'::jsonb, now() - interval '2 minutes')`,
		userID, questionID, knowledgePointID, materialID); err != nil {
		t.Fatalf("insert evidence: %v", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into mastery (user_id, knowledge_point_id, state, evidence_count, rule_version)
		values ($1::uuid, $2, 'independent', 1, 'v1')`, userID, knowledgePointID); err != nil {
		t.Fatalf("insert mastery: %v", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into review_items (user_id, question_id, due_at, interval_days, reason)
		values ($1::uuid, $2, now() + interval '2 days', 2, '{"rule":"assisted"}'::jsonb)`,
		userID, questionID); err != nil {
		t.Fatalf("insert review item: %v", err)
	}

	taskID := "77777777-7777-7777-7777-777777777777"
	if _, err := db.Pool.Exec(t.Context(), `
		insert into ai_tasks (id, user_id, kind, status, context, idempotency_key, attempt,
			created_at, started_at, finished_at)
		values ($1::uuid, $2::uuid, 'hint', 'completed', '{"questionId":"q-v2"}'::jsonb, 'orig-key', 1,
			now() - interval '5 minutes', now() - interval '5 minutes', now() - interval '4 minutes')`,
		taskID, userID); err != nil {
		t.Fatalf("insert ai task: %v", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into ai_events (task_id, seq, type, payload) values ($1::uuid, 1, 'accepted', '{}'::jsonb)`,
		taskID); err != nil {
		t.Fatalf("insert ai event: %v", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into ai_messages (id, task_id, role, content)
		values ('88888888-8888-8888-8888-888888888888', $1::uuid, 'user', '你好')`, taskID); err != nil {
		t.Fatalf("insert ai message: %v", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into teaching_identities (user_id, teaching_user_id) values ($1::uuid, $2)`,
		userID, "teach-"+userID); err != nil {
		t.Fatalf("insert teaching identity: %v", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into imported_records (id, user_id, source, kind, external_id, payload, note)
		values ('99999999-9999-9999-9999-999999999999', $1::uuid, 'rka-v1', 'chat', 'chat-1', '{"messages":[]}'::jsonb, '旧对话')`,
		userID); err != nil {
		t.Fatalf("insert imported record: %v", err)
	}
}

func userDataCounts(t *testing.T, db *store.DB, userID string) map[string]int {
	t.Helper()
	queries := map[string]string{
		"attempts":         `select count(*) from attempts where user_id = $1::uuid`,
		"marks":            `select count(*) from marks where user_id = $1::uuid`,
		"sessions":         `select count(*) from practice_sessions where user_id = $1::uuid`,
		"reading":          `select count(*) from reading_progress where user_id = $1::uuid`,
		"annotations":      `select count(*) from annotations where user_id = $1::uuid`,
		"writingDocuments": `select count(*) from writing_documents where user_id = $1::uuid`,
		"writingVersions":  `select count(*) from writing_versions v join writing_documents d on d.id = v.document_id where d.user_id = $1::uuid`,
		"writingReviews":   `select count(*) from writing_reviews r join writing_documents d on d.id = r.document_id where d.user_id = $1::uuid`,
		"evidence":         `select count(*) from evidence where user_id = $1::uuid`,
		"mastery":          `select count(*) from mastery where user_id = $1::uuid`,
		"reviewItems":      `select count(*) from review_items where user_id = $1::uuid`,
		"aiTasks":          `select count(*) from ai_tasks where user_id = $1::uuid`,
		"aiEvents":         `select count(*) from ai_events e join ai_tasks t on t.id = e.task_id where t.user_id = $1::uuid`,
		"aiMessages":       `select count(*) from ai_messages m join ai_tasks t on t.id = m.task_id where t.user_id = $1::uuid`,
		"importedRecords":  `select count(*) from imported_records where user_id = $1::uuid`,
		"teachingIdentity": `select count(*) from teaching_identities where user_id = $1::uuid`,
	}
	counts := map[string]int{}
	for name, query := range queries {
		var count int
		if err := db.Pool.QueryRow(t.Context(), query, userID).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		counts[name] = count
	}
	return counts
}

func TestExportDeleteImportV2RestoresAndIsIdempotent(t *testing.T) {
	application, server, db := testutil.NewApp(t)
	user := testutil.CreateUser(t, application, "learner@example.com", password, "learner")
	testutil.Login(t, server, "learner@example.com", password)
	seedV2AccountData(t, db, user.ID, "material-v2", "q-v2", "kp-v2")

	before := userDataCounts(t, db, user.ID)
	for name, count := range before {
		if count == 0 {
			t.Fatalf("seed data missing for %s", name)
		}
	}

	status, exported := server.Do(t, http.MethodGet, "/api/export/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, exported)
	if exported["schemaVersion"] != float64(2) {
		t.Fatalf("export schemaVersion = %v, want 2", exported["schemaVersion"])
	}
	data, _ := exported["data"].(map[string]any)
	if data == nil {
		t.Fatalf("export data missing: %v", exported)
	}
	for _, category := range []string{"attempts", "marks", "sessions", "reading", "annotations",
		"writingDocuments", "writingVersions", "writingReviews", "evidence", "mastery",
		"reviewItems", "aiTasks", "aiEvents", "aiMessages", "importedRecords", "teachingIdentity"} {
		rows, _ := data[category].([]any)
		if len(rows) == 0 {
			t.Fatalf("export category %s is empty", category)
		}
	}

	status, body := server.Do(t, http.MethodDelete, "/api/me/data", map[string]any{"confirm": "DELETE"})
	testutil.MustStatus(t, http.StatusOK, status, body)
	empty := userDataCounts(t, db, user.ID)
	for name, count := range empty {
		if count != 0 {
			t.Fatalf("delete left %s rows: %d", name, count)
		}
	}

	status, body = server.Do(t, http.MethodPost, "/api/sync/import", exported)
	testutil.MustStatus(t, http.StatusOK, status, body)
	imported, _ := body["imported"].(map[string]any)
	for name, want := range before {
		got, _ := imported[name].(float64)
		if int(got) != want {
			t.Fatalf("imported[%s] = %v, want %d (body=%v)", name, imported[name], want, body)
		}
	}
	restored := userDataCounts(t, db, user.ID)
	for name, want := range before {
		if restored[name] != want {
			t.Fatalf("restored %s = %d, want %d", name, restored[name], want)
		}
	}

	// Re-importing the same export must not duplicate anything.
	status, body = server.Do(t, http.MethodPost, "/api/sync/import", exported)
	testutil.MustStatus(t, http.StatusOK, status, body)
	imported, _ = body["imported"].(map[string]any)
	total := 0
	for _, value := range imported {
		if number, ok := value.(float64); ok {
			total += int(number)
		}
	}
	if total != 0 {
		t.Fatalf("re-import wrote duplicates: %v", imported)
	}
	counts := body["counts"].(map[string]any)
	if counts["skipped"] == float64(0) {
		t.Fatalf("re-import should report skipped duplicates: %v", body)
	}
	afterRepeat := userDataCounts(t, db, user.ID)
	for name, want := range before {
		if afterRepeat[name] != want {
			t.Fatalf("re-import changed %s: %d != %d", name, afterRepeat[name], want)
		}
	}

	// A newer server-side draft must never be overwritten by the older export;
	// both versions are kept and the conflict is reported.
	documentID := "22222222-2222-2222-2222-222222222222"
	if _, err := db.Pool.Exec(t.Context(), `
		update writing_documents set values = '{"answer-0":"服务器上的更新版本"}'::jsonb, version = 3, updated_at = now()
		where id = $1::uuid`, documentID); err != nil {
		t.Fatal(err)
	}
	status, body = server.Do(t, http.MethodPost, "/api/sync/import", exported)
	testutil.MustStatus(t, http.StatusOK, status, body)
	imported, _ = body["imported"].(map[string]any)
	if imported["writingDocuments"] != float64(0) {
		t.Fatalf("older export overwrote a newer server draft: %v", imported)
	}
	conflicts, _ := body["conflicts"].([]any)
	if len(conflicts) == 0 {
		t.Fatalf("newer-server-draft conflict was not reported: %v", body)
	}
	var serverVersion int
	if err := db.Pool.QueryRow(t.Context(), `select version from writing_documents where id = $1::uuid`, documentID).Scan(&serverVersion); err != nil {
		t.Fatal(err)
	}
	if serverVersion != 3 {
		t.Fatalf("server draft version = %d, want 3 (not overwritten)", serverVersion)
	}
	var conflictVersions int
	if err := db.Pool.QueryRow(t.Context(), `
		select count(*) from writing_versions where document_id = $1::uuid and source = 'conflict'`,
		documentID).Scan(&conflictVersions); err != nil {
		t.Fatal(err)
	}
	if conflictVersions == 0 {
		t.Fatalf("conflicting exported draft content was not preserved")
	}
}

func TestImportV2RejectsOtherAccountAndUnknownSchema(t *testing.T) {
	application, serverA, db := testutil.NewApp(t)
	userA := testutil.CreateUser(t, application, "a@example.com", password, "learner")
	testutil.CreateUser(t, application, "b@example.com", password, "learner")
	testutil.Login(t, serverA, "a@example.com", password)
	seedV2AccountData(t, db, userA.ID, "material-v2", "q-v2", "kp-v2")

	status, exported := serverA.Do(t, http.MethodGet, "/api/export/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, exported)

	// Second browser session for the other account.
	_, serverB, _ := testutil.NewAppFor(t, application)
	testutil.Login(t, serverB, "b@example.com", password)
	status, body := serverB.Do(t, http.MethodPost, "/api/sync/import", exported)
	testutil.MustStatus(t, http.StatusBadRequest, status, body)
	if code := errorCode(body); code != "foreign_backup" {
		t.Fatalf("cross-user import code = %q, want foreign_backup; body=%v", code, body)
	}
	message, _ := body["error"].(map[string]any)["message"].(string)
	if message == "" {
		t.Fatalf("cross-user rejection must explain the reason")
	}
	var otherCount int
	if err := db.Pool.QueryRow(t.Context(), `select count(*) from attempts where user_id <> $1::uuid`, userA.ID).Scan(&otherCount); err != nil {
		t.Fatal(err)
	}
	if otherCount != 0 {
		t.Fatalf("cross-user import wrote %d attempts", otherCount)
	}
	// Account A's data is untouched by the rejected request.
	if counts := userDataCounts(t, db, userA.ID); counts["attempts"] != 1 {
		t.Fatalf("account A data was modified: %v", counts)
	}

	// Unknown schema versions are rejected with an explicit reason.
	status, body = serverB.Do(t, http.MethodPost, "/api/sync/import", map[string]any{
		"app": "rka", "schemaVersion": 99, "data": map[string]any{},
	})
	testutil.MustStatus(t, http.StatusBadRequest, status, body)
	if code := errorCode(body); code != "unsupported_schema" {
		t.Fatalf("unknown schema code = %q, want unsupported_schema", code)
	}
	message, _ = body["error"].(map[string]any)["message"].(string)
	if !strings.Contains(message, "schemaVersion") || !strings.Contains(message, "99") {
		t.Fatalf("unknown schema message unclear: %q", message)
	}
}

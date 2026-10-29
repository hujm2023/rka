package evidence_test

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"rka/internal/app"
	"rka/internal/auth"
	"rka/internal/store"
	"rka/internal/testutil"
)

const password = "correct-horse-battery"

// ---------------------------------------------------------------- fixtures

type fixture struct {
	app    *app.App
	server *testutil.Server
	db     *store.DB
	user   auth.User
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	application, server, db := testutil.NewApp(t)
	user := testutil.CreateUser(t, application, "learner@example.com", password, "learner")
	testutil.Login(t, server, "learner@example.com", password)
	return &fixture{app: application, server: server, db: db, user: user}
}

// seedMaterial inserts a material with one version and two sections, returning
// the version id.
func seedMaterial(t *testing.T, db *store.DB, materialID, reviewStatus string) string {
	t.Helper()
	ctx := t.Context()
	sum := sha256.Sum256([]byte("material:" + materialID))
	sha := fmt.Sprintf("%x", sum)
	if _, err := db.Pool.Exec(ctx, `
		insert into blobs (sha256, size, rel_path) values ($1, 0, $2)
		on conflict (sha256) do nothing`, sha, "originals/"+materialID+".md"); err != nil {
		t.Fatalf("seed blob: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		insert into materials (id, title, book, kind, module, source_file, review_status)
		values ($1, $2, '测试教材', 'textbook', 'other', 'test.md', $3)`,
		materialID, materialID, reviewStatus); err != nil {
		t.Fatalf("seed material: %v", err)
	}
	var versionID string
	if err := db.Pool.QueryRow(ctx, `
		insert into material_versions (material_id, version, sha256, chars, minutes)
		values ($1, 1, $2, 1200, 5) returning id::text`, materialID, sha).Scan(&versionID); err != nil {
		t.Fatalf("seed material version: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		update materials set current_version_id = $2::uuid where id = $1`, materialID, versionID); err != nil {
		t.Fatalf("seed current version: %v", err)
	}
	sections := []struct {
		anchor, key, title string
		start              int
	}{
		{"section-0", "sec-intro", "引言", 0},
		{"section-100", "sec-core", "核心内容", 100},
	}
	for _, section := range sections {
		if _, err := db.Pool.Exec(ctx, `
			insert into material_sections (material_version_id, anchor, stable_key, title, level, start_offset, path)
			values ($1::uuid, $2, $3, $4, 1, $5, array[$2])`,
			versionID, section.anchor, section.key, section.title, section.start); err != nil {
			t.Fatalf("seed material section: %v", err)
		}
	}
	return versionID
}

func seedKnowledgePoint(t *testing.T, db *store.DB, id, name string) {
	t.Helper()
	if _, err := db.Pool.Exec(t.Context(), `
		insert into knowledge_points (id, name, module) values ($1, $2, 'other')`, id, name); err != nil {
		t.Fatalf("seed knowledge point: %v", err)
	}
}

func seedQuestion(t *testing.T, db *store.DB, questionID string, knowledgePointIDs ...string) {
	t.Helper()
	ctx := t.Context()
	if _, err := db.Pool.Exec(ctx, `
		insert into questions (id, source_type, module) values ($1, 'mock', 'other')`, questionID); err != nil {
		t.Fatalf("seed question: %v", err)
	}
	var versionID string
	if err := db.Pool.QueryRow(ctx, `
		insert into question_versions (question_id, revision, stem, options, answer)
		values ($1, 'r1', '题干', '{"A":"甲","B":"乙"}'::jsonb, 'A') returning id::text`, questionID).Scan(&versionID); err != nil {
		t.Fatalf("seed question version: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		update questions set current_version_id = $2::uuid where id = $1`, questionID, versionID); err != nil {
		t.Fatalf("seed question current version: %v", err)
	}
	for _, kpID := range knowledgePointIDs {
		if _, err := db.Pool.Exec(ctx, `
			insert into question_knowledge_points (question_id, knowledge_point_id, source, confidence, status)
			values ($1, $2, 'manual', 1, 'approved')`, questionID, kpID); err != nil {
			t.Fatalf("seed question knowledge point: %v", err)
		}
	}
}

func seedKnowledgeLink(t *testing.T, db *store.DB, questionID, materialID, versionID, anchor string) {
	t.Helper()
	if _, err := db.Pool.Exec(t.Context(), `
		insert into knowledge_links
			(question_id, material_id, material_version_id, section_id, section_stable_key, status)
		select $1, $2, $3::uuid, ms.id, ms.stable_key, 'approved'
		from material_sections ms
		where ms.material_version_id = $3::uuid and ms.anchor = $4`, questionID, materialID, versionID, anchor); err != nil {
		t.Fatalf("seed knowledge link: %v", err)
	}
}

func seedAttempt(t *testing.T, db *store.DB, userID, attemptID, questionID string, correct, assisted bool, answeredAt time.Time) {
	t.Helper()
	if _, err := db.Pool.Exec(t.Context(), `
		insert into attempts
			(id, user_id, question_id, question_revision, selected, correct, assisted, duration_ms, answered_at)
		values ($1, $2::uuid, $3, 'r1', 'A', $4, $5, 3000, $6)`,
		attemptID, userID, questionID, correct, assisted, answeredAt); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
}

// ---------------------------------------------------------------- assertions

func errorCode(body map[string]any) string {
	envelope, _ := body["error"].(map[string]any)
	code, _ := envelope["code"].(string)
	return code
}

func intValue(t *testing.T, value any) int {
	t.Helper()
	number, ok := value.(float64)
	if !ok {
		t.Fatalf("expected number, got %T (%v)", value, value)
	}
	return int(number)
}

func countsOf(t *testing.T, body map[string]any) map[string]int {
	t.Helper()
	raw, ok := body["counts"].(map[string]any)
	if !ok {
		t.Fatalf("missing counts object: %v", body)
	}
	counts := map[string]int{}
	for key, value := range raw {
		counts[key] = intValue(t, value)
	}
	return counts
}

func sliceOf(t *testing.T, body map[string]any, key string) []any {
	t.Helper()
	raw, ok := body[key]
	if !ok || raw == nil {
		t.Fatalf("missing %q: %v", key, body)
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("%q is not an array: %T", key, raw)
	}
	return items
}

func objectOf(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected object, got %T (%v)", value, value)
	}
	return object
}

// ---------------------------------------------------------------- tests

// TestSummarySeparatesEvidenceKinds is the core contract: reading, assisted
// passes, independent passes and self-declarations must be counted separately,
// and every recent item must carry an id plus a lookup-friendly object.
func TestSummarySeparatesEvidenceKinds(t *testing.T) {
	fx := newFixture(t)
	ctx := t.Context()
	now, err := fx.db.Now(ctx)
	if err != nil {
		t.Fatalf("database time: %v", err)
	}
	versionID := seedMaterial(t, fx.db, "material-summary", "published")
	seedKnowledgePoint(t, fx.db, "kp-summary", "进程与线程")
	seedQuestion(t, fx.db, "q-summary", "kp-summary")

	seedAttempt(t, fx.db, fx.user.ID, "att-wrong", "q-summary", false, false, now.Add(-3*time.Hour))
	seedAttempt(t, fx.db, fx.user.ID, "att-assisted", "q-summary", true, true, now.Add(-2*time.Hour))
	seedAttempt(t, fx.db, fx.user.ID, "att-independent-1", "q-summary", true, false, now.Add(-90*time.Minute))
	seedAttempt(t, fx.db, fx.user.ID, "att-independent-2", "q-summary", true, false, now.Add(-80*time.Minute))
	// hint_used evidence is produced by the teaching/practice flow; seed one to
	// prove the summary counts each kind independently.
	if _, err := fx.db.Pool.Exec(ctx, `
		insert into evidence (user_id, kind, question_id, rule_version, payload, occurred_at)
		values ($1::uuid, 'hint_used', 'q-summary', 'v1', '{"hint":"提示"}'::jsonb, $2)`,
		fx.user.ID, now.Add(-5*time.Minute)); err != nil {
		t.Fatalf("seed hint evidence: %v", err)
	}

	status, body := fx.server.Do(t, http.MethodPost, "/api/learning/reading", map[string]any{
		"materialId": "material-summary", "materialVersionId": versionID, "progress": 0.2,
		"sectionId": "section-0", "sectionKey": "sec-intro", "offset": 0, "done": false,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["deduplicated"] != false {
		t.Fatalf("first reading report should not be deduplicated: %v", body)
	}
	status, body = fx.server.Do(t, http.MethodPost, "/api/learning/reading", map[string]any{
		"materialId": "material-summary", "materialVersionId": versionID, "progress": 0.95,
		"sectionId": "section-100", "sectionKey": "sec-core", "offset": 120, "done": true,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)

	status, body = fx.server.Do(t, http.MethodPost, "/api/learning/self-declared", map[string]any{
		"knowledgePointId": "kp-summary", "mastered": true,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)

	status, summary := fx.server.Do(t, http.MethodGet, "/api/learning/summary", nil)
	testutil.MustStatus(t, http.StatusOK, status, summary)
	counts := countsOf(t, summary)
	want := map[string]int{
		"reading": 1, "reading_updates": 2, "hint_used": 1, "assisted_pass": 1,
		"independent_pass": 2, "wrong": 1, "self_declared": 1,
	}
	for key, expected := range want {
		if counts[key] != expected {
			t.Fatalf("counts[%q] = %d, want %d (all counts %v)", key, counts[key], expected, counts)
		}
	}

	recent := sliceOf(t, summary, "recent")
	if len(recent) != 8 {
		t.Fatalf("recent evidence = %d, want 8: %v", len(recent), recent)
	}
	for index, raw := range recent {
		item := objectOf(t, raw)
		if id, _ := item["id"].(string); id == "" {
			t.Fatalf("recent[%d] has no id: %v", index, item)
		}
		if _, err := time.Parse(time.RFC3339Nano, fmt.Sprint(item["occurredAt"])); err != nil {
			// encoding/json decodes into string; tolerate the exact layout.
			if _, ok := item["occurredAt"].(string); !ok {
				t.Fatalf("recent[%d] has no occurredAt: %v", index, item)
			}
		}
		lookup := false
		for _, key := range []string{"questionId", "materialId", "knowledgePointId"} {
			if value, _ := item[key].(string); value != "" {
				lookup = true
			}
		}
		if !lookup {
			t.Fatalf("recent[%d] is not lookup-friendly: %v", index, item)
		}
	}
	first := objectOf(t, recent[0])
	if first["kind"] != "self_declared" {
		t.Fatalf("most recent evidence kind = %v, want self_declared", first["kind"])
	}

	progress := sliceOf(t, summary, "readingProgress")
	if len(progress) != 1 {
		t.Fatalf("readingProgress = %d, want 1", len(progress))
	}
	row := objectOf(t, progress[0])
	if row["materialId"] != "material-summary" || row["done"] != true {
		t.Fatalf("unexpected reading progress: %v", row)
	}
	if progressValue, _ := row["progress"].(float64); progressValue < 0.9 {
		t.Fatalf("reading progress = %v, want ~0.95", row["progress"])
	}
}

func TestReadingReportIsIdempotentWhenUnchanged(t *testing.T) {
	fx := newFixture(t)
	versionID := seedMaterial(t, fx.db, "material-idem", "published")
	payload := map[string]any{
		"materialId": "material-idem", "materialVersionId": versionID, "progress": 1.0,
		"sectionId": "section-0", "offset": 10, "done": true,
	}
	status, body := fx.server.Do(t, http.MethodPost, "/api/learning/reading", payload)
	testutil.MustStatus(t, http.StatusOK, status, body)
	status, body = fx.server.Do(t, http.MethodPost, "/api/learning/reading", payload)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["deduplicated"] != true {
		t.Fatalf("repeated identical reading report should be deduplicated: %v", body)
	}

	status, summary := fx.server.Do(t, http.MethodGet, "/api/learning/summary", nil)
	testutil.MustStatus(t, http.StatusOK, status, summary)
	if got := countsOf(t, summary)["reading"]; got != 1 {
		t.Fatalf("reading completion count = %d, want 1 after deduplicated report", got)
	}
	if got := countsOf(t, summary)["reading_updates"]; got != 1 {
		t.Fatalf("reading update count = %d, want 1 after deduplicated report", got)
	}
}

func TestSelfDeclaredIsIdempotentWhenUnchanged(t *testing.T) {
	fx := newFixture(t)
	seedKnowledgePoint(t, fx.db, "kp-idem", "重复声明")
	declaration := map[string]any{"knowledgePointId": "kp-idem", "mastered": true}
	status, body := fx.server.Do(t, http.MethodPost, "/api/learning/self-declared", declaration)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["deduplicated"] != false {
		t.Fatalf("first declaration should not be deduplicated: %v", body)
	}
	status, body = fx.server.Do(t, http.MethodPost, "/api/learning/self-declared", declaration)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["deduplicated"] != true {
		t.Fatalf("repeated identical declaration should be deduplicated: %v", body)
	}
	status, mastery := fx.server.Do(t, http.MethodGet, "/api/learning/mastery", nil)
	testutil.MustStatus(t, http.StatusOK, status, mastery)
	item := masteryStateOf(t, mastery, "kp-idem")
	if got := intValue(t, objectOf(t, item["counts"])["self_declared"]); got != 1 {
		t.Fatalf("self_declared evidence = %d, want 1 after repeated identical declaration", got)
	}
}

func TestReadingValidatesMaterialAccessAndInput(t *testing.T) {
	fx := newFixture(t)
	publishedVersion := seedMaterial(t, fx.db, "material-open", "published")
	draftVersion := seedMaterial(t, fx.db, "material-draft", "draft")

	cases := []struct {
		name   string
		body   map[string]any
		status int
		code   string
	}{
		{
			name:   "unknown material",
			body:   map[string]any{"materialId": "material-missing", "progress": 0.1},
			status: http.StatusNotFound,
			code:   "material_not_found",
		},
		{
			name:   "draft material without grant",
			body:   map[string]any{"materialId": "material-draft", "materialVersionId": draftVersion, "progress": 0.1},
			status: http.StatusForbidden,
			code:   "material_forbidden",
		},
		{
			name:   "progress out of range",
			body:   map[string]any{"materialId": "material-open", "materialVersionId": publishedVersion, "progress": 1.5},
			status: http.StatusBadRequest,
			code:   "bad_progress",
		},
		{
			name:   "version belongs to another material",
			body:   map[string]any{"materialId": "material-open", "materialVersionId": draftVersion, "progress": 0.1},
			status: http.StatusBadRequest,
			code:   "unknown_version",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := fx.server.Do(t, http.MethodPost, "/api/learning/reading", tc.body)
			testutil.MustStatus(t, tc.status, status, body)
			if code := errorCode(body); code != tc.code {
				t.Fatalf("error code = %q, want %q (body %v)", code, tc.code, body)
			}
		})
	}

	// An unknown anchor is accepted and explicitly reported as unresolved, so a
	// cached older revision does not lose the learner's progress report.
	status, body := fx.server.Do(t, http.MethodPost, "/api/learning/reading", map[string]any{
		"materialId": "material-open", "materialVersionId": publishedVersion, "progress": 0.1, "sectionId": "section-999",
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	if reading := objectOf(t, body["reading"]); reading["sectionResolved"] != false {
		t.Fatalf("unknown section should be reported as unresolved: %v", reading)
	}

	// An administrator may read a draft material.
	testutil.CreateUser(t, fx.app, "admin@example.com", password, "admin")
	testutil.Login(t, fx.server, "admin@example.com", password)
	status, body = fx.server.Do(t, http.MethodPost, "/api/learning/reading", map[string]any{
		"materialId": "material-draft", "materialVersionId": draftVersion, "progress": 0.3,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
}

func TestSelfDeclaredCanBeRevokedAndFallsBackToAttemptEvidence(t *testing.T) {
	fx := newFixture(t)
	now, err := fx.db.Now(t.Context())
	if err != nil {
		t.Fatalf("database time: %v", err)
	}
	seedKnowledgePoint(t, fx.db, "kp-declared", "架构风格")
	seedKnowledgePoint(t, fx.db, "kp-backed", "质量属性")
	seedQuestion(t, fx.db, "q-backed", "kp-backed")
	seedAttempt(t, fx.db, fx.user.ID, "att-backed", "q-backed", true, false, now.Add(-time.Hour))

	status, body := fx.server.Do(t, http.MethodPost, "/api/learning/self-declared", map[string]any{
		"knowledgePointId": "kp-declared", "mastered": true,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["state"] != "self_declared" {
		t.Fatalf("state after declaration = %v, want self_declared", body["state"])
	}
	status, body = fx.server.Do(t, http.MethodGet, "/api/learning/mastery", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	declaredState := masteryStateOf(t, body, "kp-declared")
	if declaredState["state"] != "self_declared" || declaredState["recordedState"] != "self_declared" {
		t.Fatalf("mastery after declaration = %v", declaredState)
	}

	// Revoking the declaration removes the self-declared state; with no other
	// evidence the knowledge point falls back to unknown.
	status, body = fx.server.Do(t, http.MethodPost, "/api/learning/self-declared", map[string]any{
		"knowledgePointId": "kp-declared", "mastered": false,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["state"] != "unknown" {
		t.Fatalf("state after revocation = %v, want unknown", body["state"])
	}
	status, body = fx.server.Do(t, http.MethodGet, "/api/learning/mastery", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	declaredState = masteryStateOf(t, body, "kp-declared")
	if declaredState["state"] != "unknown" || declaredState["recordedState"] != "unknown" {
		t.Fatalf("mastery after revocation = %v", declaredState)
	}
	if got := intValue(t, objectOf(t, declaredState["counts"])["self_declared"]); got != 2 {
		t.Fatalf("self_declared evidence count = %d, want 2 (declaration + revocation)", got)
	}

	// When independent evidence exists, revoking a declaration falls back to
	// that real evidence instead of unknown.
	status, body = fx.server.Do(t, http.MethodPost, "/api/learning/self-declared", map[string]any{
		"knowledgePointId": "kp-backed", "mastered": true,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["state"] != "self_declared" {
		t.Fatalf("state = %v, want self_declared before revocation", body["state"])
	}
	status, body = fx.server.Do(t, http.MethodPost, "/api/learning/self-declared", map[string]any{
		"knowledgePointId": "kp-backed", "mastered": false,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["state"] != "independent" {
		t.Fatalf("state after revoking backed declaration = %v, want independent", body["state"])
	}
}

func masteryStateOf(t *testing.T, body map[string]any, knowledgePointID string) map[string]any {
	t.Helper()
	for _, raw := range sliceOf(t, body, "items") {
		item := objectOf(t, raw)
		if item["knowledgePointId"] == knowledgePointID {
			return item
		}
	}
	t.Fatalf("knowledge point %q not present in mastery response: %v", knowledgePointID, body["items"])
	return nil
}

func TestMasteryDerivesReadFromFinishedReading(t *testing.T) {
	fx := newFixture(t)
	versionID := seedMaterial(t, fx.db, "material-read", "published")
	seedKnowledgePoint(t, fx.db, "kp-read", "软件架构")
	seedKnowledgePoint(t, fx.db, "kp-assisted", "设计模式")
	seedQuestion(t, fx.db, "q-read", "kp-read")
	seedQuestion(t, fx.db, "q-assisted", "kp-assisted")
	seedKnowledgeLink(t, fx.db, "q-read", "material-read", versionID, "section-100")
	seedKnowledgeLink(t, fx.db, "q-assisted", "material-read", versionID, "section-100")

	status, body := fx.server.Do(t, http.MethodPost, "/api/learning/reading", map[string]any{
		"materialId": "material-read", "materialVersionId": versionID, "progress": 1.0,
		"sectionId": "section-100", "done": true,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)

	now, err := fx.db.Now(t.Context())
	if err != nil {
		t.Fatalf("database time: %v", err)
	}
	seedAttempt(t, fx.db, fx.user.ID, "att-assisted", "q-assisted", true, true, now.Add(-30*time.Minute))

	status, body = fx.server.Do(t, http.MethodGet, "/api/learning/mastery", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if got := masteryStateOf(t, body, "kp-read")["state"]; got != "read" {
		t.Fatalf("kp-read state = %v, want read", got)
	}
	assisted := masteryStateOf(t, body, "kp-assisted")
	if assisted["state"] != "assisted" {
		t.Fatalf("kp-assisted state = %v, want assisted", assisted["state"])
	}
	counts := objectOf(t, assisted["counts"])
	if intValue(t, counts["reading"]) != 1 || intValue(t, counts["assisted_pass"]) != 1 {
		t.Fatalf("kp-assisted counts = %v, want reading=1 assisted_pass=1", counts)
	}

	// Untouched knowledge points are only listed when explicitly requested.
	seedKnowledgePoint(t, fx.db, "kp-untouched", "未学习知识点")
	status, body = fx.server.Do(t, http.MethodGet, "/api/learning/mastery?includeUnknown=1", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if got := masteryStateOf(t, body, "kp-untouched")["state"]; got != "unknown" {
		t.Fatalf("kp-untouched state = %v, want unknown", got)
	}
}

func TestReviewPlanDerivesDueItemsFromLatestAttemptsOnly(t *testing.T) {
	fx := newFixture(t)
	now, err := fx.db.Now(t.Context())
	if err != nil {
		t.Fatalf("database time: %v", err)
	}
	for _, questionID := range []string{"q-wrong", "q-assisted", "q-independent", "q-reconfirmed"} {
		seedQuestion(t, fx.db, questionID)
	}
	seedAttempt(t, fx.db, fx.user.ID, "att-wrong", "q-wrong", false, false, now.Add(-48*time.Hour))
	seedAttempt(t, fx.db, fx.user.ID, "att-assisted", "q-assisted", true, true, now.Add(-72*time.Hour))
	seedAttempt(t, fx.db, fx.user.ID, "att-independent", "q-independent", true, false, now.Add(-time.Hour))
	// A wrong answer ten days ago is superseded by a recent independent pass.
	seedAttempt(t, fx.db, fx.user.ID, "att-old-wrong", "q-reconfirmed", false, false, now.Add(-10*24*time.Hour))
	seedAttempt(t, fx.db, fx.user.ID, "att-reconfirmed", "q-reconfirmed", true, false, now.Add(-2*time.Hour))

	status, body := fx.server.Do(t, http.MethodGet, "/api/learning/review-plan", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items := sliceOf(t, body, "items")
	if len(items) != 2 {
		t.Fatalf("due items = %d, want 2: %v", len(items), items)
	}
	if got := intValue(t, body["dueCount"]); got != 2 {
		t.Fatalf("dueCount = %d, want 2", got)
	}
	seen := map[string]map[string]any{}
	for _, raw := range items {
		item := objectOf(t, raw)
		seen[fmt.Sprint(item["questionId"])] = item
	}
	wrong, ok := seen["q-wrong"]
	if !ok {
		t.Fatalf("q-wrong missing from due list: %v", items)
	}
	wrongReason := objectOf(t, wrong["reason"])
	if wrongReason["code"] != "wrong" {
		t.Fatalf("wrong reason code = %v, want wrong", wrongReason["code"])
	}
	if got := intValue(t, wrongReason["intervalDays"]); got != 1 {
		t.Fatalf("wrong interval = %d, want 1", got)
	}
	if label, _ := wrongReason["label"].(string); label == "" {
		t.Fatalf("wrong reason has no explanation: %v", wrongReason)
	}
	if got := intValue(t, wrongReason["overdueDays"]); got < 1 {
		t.Fatalf("wrong overdueDays = %d, want >= 1", got)
	}
	assisted, ok := seen["q-assisted"]
	if !ok {
		t.Fatalf("q-assisted missing from due list: %v", items)
	}
	if code := objectOf(t, assisted["reason"])["code"]; code != "assisted_pass" {
		t.Fatalf("assisted reason code = %v, want assisted_pass", code)
	}
	if _, ok := seen["q-independent"]; ok {
		t.Fatal("independent pass answered an hour ago is not due yet")
	}
	if _, ok := seen["q-reconfirmed"]; ok {
		t.Fatal("an old wrong answer must not override a newer independent pass")
	}

	// Review planning is read-only: no review_items rows and no attempt changes.
	var reviewRows, attemptRows int
	if err := fx.db.Pool.QueryRow(t.Context(), `select count(*) from review_items where user_id = $1::uuid`, fx.user.ID).Scan(&reviewRows); err != nil {
		t.Fatalf("count review items: %v", err)
	}
	if reviewRows != 0 {
		t.Fatalf("review plan wrote %d review_items rows; it must stay read-only", reviewRows)
	}
	if err := fx.db.Pool.QueryRow(t.Context(), `select count(*) from attempts where user_id = $1::uuid`, fx.user.ID).Scan(&attemptRows); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if attemptRows != 5 {
		t.Fatalf("attempts = %d, want 5 untouched facts", attemptRows)
	}
}

func TestReportIsTraceableAndDoesNotInventPassRate(t *testing.T) {
	fx := newFixture(t)
	now, err := fx.db.Now(t.Context())
	if err != nil {
		t.Fatalf("database time: %v", err)
	}
	for _, questionID := range []string{"q-wrong", "q-assisted", "q-independent", "q-due"} {
		seedQuestion(t, fx.db, questionID)
	}
	seedAttempt(t, fx.db, fx.user.ID, "att-wrong", "q-wrong", false, false, now.Add(-2*24*time.Hour))
	seedAttempt(t, fx.db, fx.user.ID, "att-assisted", "q-assisted", true, true, now.Add(-2*time.Hour))
	seedAttempt(t, fx.db, fx.user.ID, "att-independent-1", "q-independent", true, false, now.Add(-2*time.Hour))
	seedAttempt(t, fx.db, fx.user.ID, "att-independent-2", "q-independent", true, false, now.Add(-time.Hour))
	seedAttempt(t, fx.db, fx.user.ID, "att-due", "q-due", false, false, now.Add(-3*24*time.Hour))

	status, body := fx.server.Do(t, http.MethodGet, "/api/learning/report", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)

	conclusion := objectOf(t, body["conclusion"])
	if conclusion["focus"] != "错题复盘" {
		t.Fatalf("focus = %v, want 错题复盘", conclusion["focus"])
	}
	statements := sliceOf(t, conclusion, "evidence")
	steps := sliceOf(t, conclusion, "suggestedSteps")
	if len(steps) == 0 {
		t.Fatal("report has no suggested steps")
	}

	basis := sliceOf(t, body, "basis")
	if len(basis) != len(statements) {
		t.Fatalf("basis statements = %d, want %d (one per conclusion statement)", len(basis), len(statements))
	}
	evidenceIDs := map[string]bool{}
	for _, raw := range sliceOf(t, body, "evidence") {
		item := objectOf(t, raw)
		id, _ := item["id"].(string)
		if id == "" {
			t.Fatalf("report evidence without id: %v", item)
		}
		evidenceIDs[id] = true
		lookup := false
		for _, key := range []string{"questionId", "materialId", "knowledgePointId"} {
			if value, _ := item[key].(string); value != "" {
				lookup = true
			}
		}
		if !lookup {
			t.Fatalf("report evidence is not lookup-friendly: %v", item)
		}
	}
	for index, raw := range basis {
		item := objectOf(t, raw)
		statement, _ := item["statement"].(string)
		if statement == "" {
			t.Fatalf("basis[%d] has no statement: %v", index, item)
		}
		ids := item["evidenceIds"].([]any)
		if len(ids) == 0 {
			t.Fatalf("basis[%d] (%q) has no evidence ids", index, statement)
		}
		for _, idRaw := range ids {
			id, _ := idRaw.(string)
			if !evidenceIDs[id] {
				t.Fatalf("basis[%d] references evidence id %q that is not in report evidence", index, id)
			}
		}
	}
	reportCounts := map[string]int{}
	for key, value := range objectOf(t, body["counts"]) {
		reportCounts[key] = intValue(t, value)
	}
	if reportCounts["wrong"] != 2 || reportCounts["assisted_pass"] != 1 || reportCounts["independent_pass"] != 2 || reportCounts["due"] != 2 {
		t.Fatalf("report counts = %v, want wrong=2 assisted_pass=1 independent_pass=2 due=2", reportCounts)
	}

	calibration := objectOf(t, body["calibration"])
	if calibration["calibrated"] != false {
		t.Fatalf("calibration must be marked uncalibrated: %v", calibration)
	}
	assertNoPassRate(t, body, "")
}

func assertNoPassRate(t *testing.T, value any, path string) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "passrate") || lower == "rate" {
				t.Fatalf("response contains a pass-rate field at %s.%s", path, key)
			}
			assertNoPassRate(t, child, path+"."+key)
		}
	case []any:
		for index, child := range typed {
			assertNoPassRate(t, child, fmt.Sprintf("%s[%d]", path, index))
		}
	}
}

func TestReportIsConservativeWithoutAnyData(t *testing.T) {
	fx := newFixture(t)

	status, report := fx.server.Do(t, http.MethodGet, "/api/learning/report", nil)
	testutil.MustStatus(t, http.StatusOK, status, report)
	conclusion := objectOf(t, report["conclusion"])
	if conclusion["focus"] != "继续阅读与练习" {
		t.Fatalf("empty report focus = %v, want 继续阅读与练习", conclusion["focus"])
	}
	if got := len(sliceOf(t, report, "evidence")); got != 0 {
		t.Fatalf("empty report evidence = %d, want 0", got)
	}
	if got := len(sliceOf(t, report, "basis")); got != 0 {
		t.Fatalf("empty report basis = %d, want 0", got)
	}
	reportCounts := objectOf(t, report["counts"])
	for _, key := range []string{"wrong", "assisted_pass", "independent_pass", "due"} {
		if got := intValue(t, reportCounts[key]); got != 0 {
			t.Fatalf("empty report counts[%q] = %d, want 0", key, got)
		}
	}

	status, summary := fx.server.Do(t, http.MethodGet, "/api/learning/summary", nil)
	testutil.MustStatus(t, http.StatusOK, status, summary)
	for key, value := range countsOf(t, summary) {
		if value != 0 {
			t.Fatalf("empty summary counts[%q] = %d, want 0", key, value)
		}
	}
	if got := len(sliceOf(t, summary, "recent")); got != 0 {
		t.Fatalf("empty summary recent = %d, want 0", got)
	}
	if got := len(sliceOf(t, summary, "readingProgress")); got != 0 {
		t.Fatalf("empty summary readingProgress = %d, want 0", got)
	}

	status, mastery := fx.server.Do(t, http.MethodGet, "/api/learning/mastery", nil)
	testutil.MustStatus(t, http.StatusOK, status, mastery)
	if got := len(sliceOf(t, mastery, "items")); got != 0 {
		t.Fatalf("empty mastery items = %d, want 0", got)
	}
	status, plan := fx.server.Do(t, http.MethodGet, "/api/learning/review-plan", nil)
	testutil.MustStatus(t, http.StatusOK, status, plan)
	if got := len(sliceOf(t, plan, "items")); got != 0 {
		t.Fatalf("empty review plan items = %d, want 0", got)
	}
}

// TestEvidenceIsDerivedFromAttemptsWithoutProjectionRows proves the package is
// decoupled from any evidence-write hook: answer facts alone produce counts and
// a traceable report even when no evidence row exists for them.
func TestEvidenceIsDerivedFromAttemptsWithoutProjectionRows(t *testing.T) {
	fx := newFixture(t)
	now, err := fx.db.Now(t.Context())
	if err != nil {
		t.Fatalf("database time: %v", err)
	}
	seedKnowledgePoint(t, fx.db, "kp-facts", "系统架构")
	seedQuestion(t, fx.db, "q-facts", "kp-facts")
	seedAttempt(t, fx.db, fx.user.ID, "att-facts", "q-facts", false, false, now.Add(-2*time.Hour))

	var evidenceRows int
	if err := fx.db.Pool.QueryRow(t.Context(), `select count(*) from evidence where user_id = $1::uuid`, fx.user.ID).Scan(&evidenceRows); err != nil {
		t.Fatalf("count evidence: %v", err)
	}
	if evidenceRows != 0 {
		t.Fatalf("fixture unexpectedly has %d evidence rows", evidenceRows)
	}

	status, summary := fx.server.Do(t, http.MethodGet, "/api/learning/summary", nil)
	testutil.MustStatus(t, http.StatusOK, status, summary)
	if got := countsOf(t, summary)["wrong"]; got != 1 {
		t.Fatalf("wrong count from attempts = %d, want 1", got)
	}
	status, report := fx.server.Do(t, http.MethodGet, "/api/learning/report", nil)
	testutil.MustStatus(t, http.StatusOK, status, report)
	if focus := objectOf(t, report["conclusion"])["focus"]; focus != "错题复盘" {
		t.Fatalf("report focus = %v, want 错题复盘", focus)
	}
}

func TestLearningDataIsIsolatedPerUser(t *testing.T) {
	fx := newFixture(t)
	now, err := fx.db.Now(t.Context())
	if err != nil {
		t.Fatalf("database time: %v", err)
	}
	versionID := seedMaterial(t, fx.db, "material-owner", "published")
	seedKnowledgePoint(t, fx.db, "kp-owner", "私有知识点")
	seedQuestion(t, fx.db, "q-owner", "kp-owner")
	seedAttempt(t, fx.db, fx.user.ID, "att-owner", "q-owner", false, false, now.Add(-48*time.Hour))
	status, body := fx.server.Do(t, http.MethodPost, "/api/learning/reading", map[string]any{
		"materialId": "material-owner", "materialVersionId": versionID, "progress": 0.5, "sectionId": "section-0",
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	status, body = fx.server.Do(t, http.MethodPost, "/api/learning/self-declared", map[string]any{
		"knowledgePointId": "kp-owner", "mastered": true,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)

	// Second learner on a separate cookie jar, same application and database.
	other := testutil.CreateUser(t, fx.app, "other@example.com", password, "learner")
	_, otherServer, _ := testutil.NewAppFor(t, fx.app)
	testutil.Login(t, otherServer, "other@example.com", password)

	status, summary := otherServer.Do(t, http.MethodGet, "/api/learning/summary", nil)
	testutil.MustStatus(t, http.StatusOK, status, summary)
	for key, value := range countsOf(t, summary) {
		if value != 0 {
			t.Fatalf("other user's counts[%q] = %d, want 0", key, value)
		}
	}
	if got := len(sliceOf(t, summary, "recent")); got != 0 {
		t.Fatalf("other user sees %d recent evidence items", got)
	}
	if got := len(sliceOf(t, summary, "readingProgress")); got != 0 {
		t.Fatalf("other user sees %d reading progress rows", got)
	}
	status, mastery := otherServer.Do(t, http.MethodGet, "/api/learning/mastery", nil)
	testutil.MustStatus(t, http.StatusOK, status, mastery)
	if got := len(sliceOf(t, mastery, "items")); got != 0 {
		t.Fatalf("other user sees %d mastery items", got)
	}
	status, plan := otherServer.Do(t, http.MethodGet, "/api/learning/review-plan", nil)
	testutil.MustStatus(t, http.StatusOK, status, plan)
	if got := len(sliceOf(t, plan, "items")); got != 0 {
		t.Fatalf("other user sees %d review items", got)
	}
	status, report := otherServer.Do(t, http.MethodGet, "/api/learning/report", nil)
	testutil.MustStatus(t, http.StatusOK, status, report)
	if focus := objectOf(t, report["conclusion"])["focus"]; focus != "继续阅读与练习" {
		t.Fatalf("other user's report is not conservative: %v", focus)
	}
	if other.ID == fx.user.ID {
		t.Fatal("fixture created the same user twice")
	}
}

func TestLearningEndpointsRequireLogin(t *testing.T) {
	_, server, _ := testutil.NewApp(t)
	gets := []string{
		"/api/learning/summary",
		"/api/learning/report",
		"/api/learning/mastery",
		"/api/learning/review-plan",
	}
	for _, path := range gets {
		status, body := server.Do(t, http.MethodGet, path, nil)
		testutil.MustStatus(t, http.StatusUnauthorized, status, body)
		if code := errorCode(body); code != "unauthorized" {
			t.Fatalf("%s error code = %q, want unauthorized", path, code)
		}
	}
	status, body := server.Do(t, http.MethodPost, "/api/learning/reading", map[string]any{"materialId": "x"})
	testutil.MustStatus(t, http.StatusUnauthorized, status, body)
	status, body = server.Do(t, http.MethodPost, "/api/learning/self-declared", map[string]any{"knowledgePointId": "x", "mastered": true})
	testutil.MustStatus(t, http.StatusUnauthorized, status, body)
}

// TestSummaryCountsAttemptsRecordedByPractice proves the evidence summary reads
// the real practice fact log: answers saved through the practice HTTP API show
// up without any evidence projection step on the practice side.
func TestSummaryCountsAttemptsRecordedByPractice(t *testing.T) {
	fx := newFixture(t)
	seedQuestion(t, fx.db, "q-flow-1")
	seedQuestion(t, fx.db, "q-flow-2")

	status, body := fx.server.Do(t, http.MethodPost, "/api/practice/sessions", map[string]any{
		"mode": "all", "count": 2,
	})
	testutil.MustStatus(t, http.StatusCreated, status, body)
	session := objectOf(t, body["session"])
	sessionID, _ := session["id"].(string)
	if sessionID == "" {
		t.Fatalf("practice session has no id: %v", session)
	}
	questionIDs := sliceOf(t, session, "questionIds")
	if len(questionIDs) != 2 {
		t.Fatalf("practice queue = %d questions, want 2", len(questionIDs))
	}

	answers := []map[string]any{
		{
			"id": "evt-flow-assisted", "questionId": questionIDs[0], "selected": "A",
			"assisted": true, "durationMs": 1200, "answeredAt": time.Now().UTC().Format(time.RFC3339),
		},
		{
			"id": "evt-flow-wrong", "questionId": questionIDs[1], "selected": "B",
			"assisted": false, "durationMs": 1800, "answeredAt": time.Now().UTC().Format(time.RFC3339),
		},
	}
	for _, answer := range answers {
		status, body = fx.server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/answers", answer)
		testutil.MustStatus(t, http.StatusOK, status, body)
	}

	status, summary := fx.server.Do(t, http.MethodGet, "/api/learning/summary", nil)
	testutil.MustStatus(t, http.StatusOK, status, summary)
	counts := countsOf(t, summary)
	if counts["assisted_pass"] != 1 || counts["wrong"] != 1 || counts["independent_pass"] != 0 {
		t.Fatalf("summary did not reflect practice facts: %v", counts)
	}
	recentIDs := map[string]bool{}
	for _, raw := range sliceOf(t, summary, "recent") {
		item := objectOf(t, raw)
		recentIDs[fmt.Sprint(item["id"])] = true
	}
	for _, eventID := range []string{"evt-flow-assisted", "evt-flow-wrong"} {
		if !recentIDs[eventID] {
			t.Fatalf("recent evidence is missing practice attempt %q: %v", eventID, recentIDs)
		}
	}
}

// TestPracticeAnswersSurviveEvidenceOutage is the decoupling contract: evidence
// derivation and its table are not part of the practice write path, so an
// evidence failure must never roll back an answer that practice already saved.
func TestPracticeAnswersSurviveEvidenceOutage(t *testing.T) {
	fx := newFixture(t)
	seedQuestion(t, fx.db, "q-outage-1")
	seedQuestion(t, fx.db, "q-outage-2")

	status, body := fx.server.Do(t, http.MethodPost, "/api/practice/sessions", map[string]any{
		"mode": "all", "count": 2,
	})
	testutil.MustStatus(t, http.StatusCreated, status, body)
	session := objectOf(t, body["session"])
	sessionID, _ := session["id"].(string)
	questionIDs := sliceOf(t, session, "questionIds")

	answer := func(eventID string, index int) {
		t.Helper()
		status, body := fx.server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/answers", map[string]any{
			"id": eventID, "questionId": questionIDs[index], "selected": "B",
			"assisted": false, "durationMs": 900, "answeredAt": time.Now().UTC().Format(time.RFC3339),
		})
		testutil.MustStatus(t, http.StatusOK, status, body)
	}
	answer("evt-outage-1", 0)

	// Simulate the evidence subsystem being unavailable (missing table).
	if _, err := fx.db.Pool.Exec(t.Context(), `alter table evidence rename to evidence_outage`); err != nil {
		t.Fatalf("simulate evidence outage: %v", err)
	}
	answer("evt-outage-2", 1)

	var attempts int
	if err := fx.db.Pool.QueryRow(t.Context(), `select count(*) from attempts where user_id = $1::uuid`, fx.user.ID).Scan(&attempts); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 saved despite evidence outage", attempts)
	}

	status, body = fx.server.Do(t, http.MethodGet, "/api/learning/summary", nil)
	if status != http.StatusInternalServerError {
		t.Fatalf("summary with missing evidence table = %d, want 500 (failure must be visible)", status)
	}

	if _, err := fx.db.Pool.Exec(t.Context(), `alter table evidence_outage rename to evidence`); err != nil {
		t.Fatalf("restore evidence table: %v", err)
	}
	status, summary := fx.server.Do(t, http.MethodGet, "/api/learning/summary", nil)
	testutil.MustStatus(t, http.StatusOK, status, summary)
	if got := countsOf(t, summary)["wrong"]; got != 2 {
		t.Fatalf("wrong count after recovery = %d, want 2", got)
	}
}

// TestReadingWithImportedContent checks the reading endpoint against the real
// bundled content snapshot: anchors, stable keys and material versions come
// from the actual importer rather than hand-written fixtures.
func TestReadingWithImportedContent(t *testing.T) {
	fx := newFixture(t)
	testutil.ImportContent(t, fx.app)

	var materialID, versionID, anchor, stableKey, knowledgePointID string
	err := fx.db.Pool.QueryRow(t.Context(), `
		select m.id, mv.id::text, ms.anchor, ms.stable_key
		from materials m
		join material_versions mv on mv.id = m.current_version_id
		join material_sections ms on ms.material_version_id = mv.id
		where m.review_status = 'published'
		order by m.id, ms.start_offset
		limit 1`).Scan(&materialID, &versionID, &anchor, &stableKey)
	if err != nil {
		t.Fatalf("select imported material section: %v", err)
	}
	if err := fx.db.Pool.QueryRow(t.Context(), `select id from knowledge_points order by id limit 1`).Scan(&knowledgePointID); err != nil {
		t.Fatalf("select imported knowledge point: %v", err)
	}

	status, body := fx.server.Do(t, http.MethodPost, "/api/learning/reading", map[string]any{
		"materialId": materialID, "materialVersionId": versionID, "progress": 0.6,
		"sectionId": anchor, "sectionKey": stableKey, "offset": 12, "done": false,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	if reading := objectOf(t, body["reading"]); reading["sectionResolved"] != true {
		t.Fatalf("imported section should resolve: %v", reading)
	}

	status, body = fx.server.Do(t, http.MethodPost, "/api/learning/self-declared", map[string]any{
		"knowledgePointId": knowledgePointID, "mastered": true,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["state"] != "self_declared" {
		t.Fatalf("self declaration on imported knowledge point = %v, want self_declared", body["state"])
	}

	status, summary := fx.server.Do(t, http.MethodGet, "/api/learning/summary", nil)
	testutil.MustStatus(t, http.StatusOK, status, summary)
	if got := countsOf(t, summary)["reading"]; got != 1 {
		t.Fatalf("reading count after imported-content report = %d, want 1", got)
	}

	// An attempt on a real question must light up its imported knowledge point.
	var attemptKP, attemptQuestion string
	if err := fx.db.Pool.QueryRow(t.Context(), `
		select qkp.knowledge_point_id, qkp.question_id
		from question_knowledge_points qkp
		where qkp.status <> 'rejected'
		order by qkp.question_id
		limit 1`).Scan(&attemptKP, &attemptQuestion); err != nil {
		t.Fatalf("select imported question knowledge point: %v", err)
	}
	now, err := fx.db.Now(t.Context())
	if err != nil {
		t.Fatalf("database time: %v", err)
	}
	seedAttempt(t, fx.db, fx.user.ID, "evt-imported-independent", attemptQuestion, true, false, now.Add(-time.Hour))
	status, mastery := fx.server.Do(t, http.MethodGet, "/api/learning/mastery", nil)
	testutil.MustStatus(t, http.StatusOK, status, mastery)
	if got := masteryStateOf(t, mastery, attemptKP)["state"]; got != "independent" {
		t.Fatalf("imported knowledge point state = %v, want independent", got)
	}
}

func TestReadingPositionDoesNotRevokeCompletion(t *testing.T) {
	f := newFixture(t)
	version := seedMaterial(t, f.db, "reading-preserve", "published")
	payload := map[string]any{"materialId": "reading-preserve", "materialVersionId": version, "progress": .7, "offset": 0, "done": true}
	status, body := f.server.Do(t, http.MethodPost, "/api/learning/reading", payload)
	testutil.MustStatus(t, http.StatusOK, status, body)
	delete(payload, "done")
	payload["progress"] = .2
	status, body = f.server.Do(t, http.MethodPost, "/api/learning/reading", payload)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["reading"].(map[string]any)["done"] != true {
		t.Fatalf("position revoked completion: %v", body)
	}
	payload["done"] = false
	status, body = f.server.Do(t, http.MethodPost, "/api/learning/reading", payload)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["reading"].(map[string]any)["done"] != false {
		t.Fatalf("explicit revoke was ignored: %v", body)
	}
}

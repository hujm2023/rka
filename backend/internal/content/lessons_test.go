package content_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"rka/internal/testutil"
)

// TestContentLessonsCompatibility covers the legacy lesson index that the old
// reader and AI citations used: after import, catalog serves lesson metadata
// without bodies, each lesson resolves to the material/section it was cut from,
// and GET /api/content/lessons/{id} returns the original markdown.
func TestContentLessonsCompatibility(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.app.Content.ImportSnapshot(t.Context(), testutil.ContentDir(t), ""); err != nil {
		t.Fatalf("import snapshot: %v", err)
	}

	// 1. Import and resolution rates.
	total := scalarInt(t, env.db, `select count(*) from content_lessons`)
	withMaterial := scalarInt(t, env.db, `select count(*) from content_lessons where material_id is not null`)
	withSection := scalarInt(t, env.db, `select count(*) from content_lessons where section_stable_key is not null`)
	t.Logf("lessons total=%d material=%d section=%d", total, withMaterial, withSection)
	if total < 309 {
		t.Fatalf("lessons imported = %d, want the bundled 329", total)
	}
	if withMaterial != total {
		t.Errorf("lessons resolved to a material = %d/%d", withMaterial, total)
	}
	if withSection < total*9/10 {
		t.Errorf("lessons resolved to a section = %d/%d, want >= 90%%", withSection, total)
	}

	// 2. Catalog carries metadata only, never bodies.
	status, _, raw := response(t, env.learner, "/api/content/catalog")
	if status != http.StatusOK {
		t.Fatalf("catalog status = %d", status)
	}
	if strings.Contains(raw, `"lessons":[`) == false {
		t.Fatalf("catalog response has no lessons array")
	}
	var catalog map[string]any
	if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	lessons, _ := catalog["lessons"].([]any)
	if len(lessons) < 309 {
		t.Fatalf("catalog lessons = %d", len(lessons))
	}
	for _, entry := range lessons {
		lesson, _ := entry.(map[string]any)
		if _, ok := lesson["body"]; ok {
			t.Errorf("catalog lesson %v leaks the body", lesson["id"])
		}
		if strings.TrimSpace(asString(lesson["title"])) == "" {
			t.Errorf("catalog lesson %v has no title", lesson["id"])
		}
	}
	metadata, err := json.Marshal(lessons)
	if err != nil {
		t.Fatalf("marshal lessons: %v", err)
	}
	t.Logf("catalog bytes=%d, lessons metadata bytes=%d (%d entries)", len(raw), len(metadata), len(lessons))

	// Choose one lesson that resolved to a section, so the returned body can be
	// checked against its content hash and title.
	var lessonID, lessonTitle string
	for _, entry := range lessons {
		lesson, _ := entry.(map[string]any)
		if asString(lesson["materialId"]) != "" && asString(lesson["sectionKey"]) != "" {
			lessonID = asString(lesson["id"])
			lessonTitle = asString(lesson["title"])
			break
		}
	}
	if lessonID == "" {
		t.Fatalf("no fully resolved lesson in catalog")
	}

	// 3. GET /api/content/lessons/{id} returns the original markdown.
	status, body := env.learner.Do(t, http.MethodGet, "/api/content/lessons/"+lessonID, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	lesson, _ := body["lesson"].(map[string]any)
	if lesson["id"] != lessonID || lesson["title"] != lessonTitle {
		t.Errorf("lesson = %v", lesson)
	}
	text := asString(lesson["body"])
	if strings.TrimSpace(text) == "" {
		t.Fatalf("lesson body is empty")
	}
	sum := sha256.Sum256([]byte(text))
	stored := scalarString(t, env.db, `select body_sha256 from content_lessons where id = $1`, lessonID)
	if hex.EncodeToString(sum[:]) != stored {
		t.Errorf("lesson body sha256 does not match body_sha256")
	}
	if lesson["materialId"] == "" || lesson["sectionKey"] == "" {
		t.Errorf("lesson metadata lost its material/section mapping: %v", lesson)
	}

	// 4. Errors: unknown lesson, anonymous request.
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/lessons/does-not-exist", nil)
	if status != http.StatusNotFound {
		t.Errorf("unknown lesson status = %d body=%v", status, body)
	}
	anonymous := mustServer(t, env.app)
	status, body = anonymous.Do(t, http.MethodGet, "/api/content/lessons/"+lessonID, nil)
	if status != http.StatusUnauthorized {
		t.Errorf("anonymous lesson status = %d body=%v", status, body)
	}

	// 5. Re-importing the same snapshot keeps exactly one row per lesson.
	before := scalarInt(t, env.db, `select count(*) from content_lessons`)
	if _, err := env.app.Content.ImportSnapshot(t.Context(), testutil.ContentDir(t), ""); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if after := scalarInt(t, env.db, `select count(*) from content_lessons`); after != before {
		t.Errorf("lessons changed on re-import: %d -> %d", before, after)
	}

	// 6. A database imported before the lessons table existed must be backfilled
	// instead of taking the "already imported" fast path.
	if _, err := env.db.Pool.Exec(t.Context(), `delete from content_lessons`); err != nil {
		t.Fatalf("clear lessons: %v", err)
	}
	result, err := env.app.Content.ImportSnapshot(t.Context(), testutil.ContentDir(t), "")
	if err != nil {
		t.Fatalf("backfill import: %v", err)
	}
	if result.Skipped {
		t.Errorf("import skipped instead of backfilling lessons")
	}
	if got := scalarInt(t, env.db, `select count(*) from content_lessons`); got != before {
		t.Errorf("lessons after backfill = %d, want %d", got, before)
	}
}

// TestLessonInheritsMaterialVisibility verifies that the legacy lesson deep
// link follows the material access rule: withdrawing a material must hide its
// lesson from learners and from the catalog index, while administrators keep
// access for review.
func TestLessonInheritsMaterialVisibility(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.app.Content.ImportSnapshot(t.Context(), testutil.ContentDir(t), ""); err != nil {
		t.Fatalf("import snapshot: %v", err)
	}
	var lessonID, materialID string
	if err := env.db.Pool.QueryRow(t.Context(), `
		select id, material_id from content_lessons
		where material_id is not null order by id limit 1`).Scan(&lessonID, &materialID); err != nil {
		t.Fatalf("pick a resolved lesson: %v", err)
	}

	status, body := env.learner.Do(t, http.MethodGet, "/api/content/lessons/"+lessonID, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)

	if _, err := env.db.Pool.Exec(t.Context(),
		`update materials set review_status = 'withdrawn' where id = $1`, materialID); err != nil {
		t.Fatalf("withdraw material: %v", err)
	}
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/lessons/"+lessonID, nil)
	if status != http.StatusForbidden {
		t.Fatalf("withdrawn lesson status = %d body=%v, want 403", status, body)
	}

	status, _, raw := response(t, env.learner, "/api/content/catalog")
	if status != http.StatusOK {
		t.Fatalf("catalog status = %d", status)
	}
	var catalog map[string]any
	if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
		t.Fatalf("decode catalog: %v", err)
	}
	lessons, _ := catalog["lessons"].([]any)
	for _, entry := range lessons {
		lesson, _ := entry.(map[string]any)
		if asString(lesson["id"]) == lessonID {
			t.Fatalf("catalog still lists the withdrawn lesson %s", lessonID)
		}
	}

	// Administrators keep access for content review.
	status, body = env.admin.Do(t, http.MethodGet, "/api/content/lessons/"+lessonID, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)

	// Publishing the material again restores learner access.
	if _, err := env.db.Pool.Exec(t.Context(),
		`update materials set review_status = 'published' where id = $1`, materialID); err != nil {
		t.Fatalf("republish material: %v", err)
	}
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/lessons/"+lessonID, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
}

package content_test

import (
	"os"
	"path/filepath"
	"testing"

	"rka/internal/store"
	"rka/internal/testutil"
)

func TestImportFixtureIsIdempotent(t *testing.T) {
	env := newTestEnv(t)
	dir := writeFixture(t, "test-v1", false)

	first, err := env.app.Content.ImportSnapshot(t.Context(), dir, "")
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if first.Version != "test-v1" || !first.Published {
		t.Fatalf("first result = %+v", first)
	}
	if len(first.Errors) != 0 {
		t.Fatalf("first import errors = %v", first.Errors)
	}
	if first.Counts["materials"] != 2 || first.Counts["questions"] != 2 {
		t.Fatalf("first counts = %+v", first.Counts)
	}

	queries := []string{
		`select count(*) from materials`,
		`select count(*) from material_versions`,
		`select count(*) from material_sections`,
		`select count(*) from questions`,
		`select count(*) from question_versions`,
		`select count(*) from knowledge_points`,
		`select count(*) from question_knowledge_points`,
		`select count(*) from knowledge_links`,
		`select count(*) from subjective_items`,
		`select count(*) from blobs`,
	}
	before := make([]int, len(queries))
	for i, query := range queries {
		before[i] = scalarInt(t, env.db, query)
	}

	second, err := env.app.Content.ImportSnapshot(t.Context(), dir, "")
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if !second.Skipped {
		t.Errorf("second import should be skipped, got %+v", second)
	}
	for i, query := range queries {
		if got := scalarInt(t, env.db, query); got != before[i] {
			t.Errorf("%s changed on repeated import: %d -> %d", query, before[i], got)
		}
	}

	// Force the per-item import path: even without the fast path no rows may be
	// duplicated and user data must survive.
	if _, err := env.db.Pool.Exec(t.Context(), `update platform_state set content_version = '' where id = 1`); err != nil {
		t.Fatalf("reset content version: %v", err)
	}
	if _, err := env.db.Pool.Exec(t.Context(),
		`update knowledge_links set status = 'approved' where question_id = 'q-1'`); err != nil {
		t.Fatalf("approve link: %v", err)
	}
	third, err := env.app.Content.ImportSnapshot(t.Context(), dir, "")
	if err != nil {
		t.Fatalf("third import: %v", err)
	}
	if third.Skipped {
		t.Errorf("third import unexpectedly skipped")
	}
	for i, query := range queries {
		if got := scalarInt(t, env.db, query); got != before[i] {
			t.Errorf("%s changed on re-import: %d -> %d", query, before[i], got)
		}
	}
	if status := scalarString(t, env.db,
		`select status from knowledge_links where question_id = 'q-1'`); status != "approved" {
		t.Errorf("re-import overwrote reviewed link status: %q", status)
	}
}

func TestImportRealSnapshot(t *testing.T) {
	env := newTestEnv(t)
	snapshot := testutil.ContentDir(t)
	result, err := env.app.Content.ImportSnapshot(t.Context(), snapshot, "")
	if err != nil {
		t.Fatalf("import real snapshot: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("real snapshot import errors: %v", result.Errors)
	}
	want := map[string]int{
		"materials":        38,
		"questions":        2389,
		"questionVersions": 2389,
		"subjectiveItems":  236,
		"knowledgeLinks":   3100,
		"lessons":          329,
	}
	for key, expected := range want {
		if result.Counts[key] != expected {
			t.Errorf("count %s = %d, want %d", key, result.Counts[key], expected)
		}
	}
	if got := scalarInt(t, env.db, `select count(*) from material_sections`); got != 2625 {
		t.Errorf("sections = %d, want 2625", got)
	}
	if got := scalarInt(t, env.db, `select count(*) from blobs`); got != 38+lessonBlobCount(t, env.db) {
		t.Errorf("blobs = %d, want 38 material originals + lesson bodies", got)
	}
	if got := scalarInt(t, env.db, `select count(*) from content_lessons where material_id is not null`); got != 329 {
		t.Errorf("lessons resolved to a material = %d, want 329", got)
	}
	if got := scalarInt(t, env.db, `select count(*) from content_lessons where section_stable_key is not null`); got < 320 {
		t.Errorf("lessons resolved to a section = %d, want >= 320", got)
	} else {
		t.Logf("lessons resolved to a section: %d/329", got)
	}
	t.Logf("blobs: materials=38 lessons=%d", lessonBlobCount(t, env.db))
	// Originals must be content addressed on disk, never inside the database.
	var relPath, sha string
	if err := env.db.Pool.QueryRow(t.Context(),
		`select rel_path, sha256 from blobs limit 1`).Scan(&relPath, &sha); err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if relPath != "materials/"+sha+".md" {
		t.Errorf("blob rel_path = %q", relPath)
	}
	if _, err := os.Stat(filepath.Join(env.app.Config.OriginalsDir(), filepath.FromSlash(relPath))); err != nil {
		t.Errorf("original file missing: %v", err)
	}
	// Derived parse products are written outside the database.
	derived := filepath.Join(env.app.Config.DerivedDir(), "content", result.Version+".json")
	if _, err := os.Stat(derived); err != nil {
		t.Errorf("derived manifest missing: %v", err)
	}
	if got := scalarString(t, env.db, `select content_version from platform_state where id = 1`); got != result.Version {
		t.Errorf("platform content_version = %q", got)
	}
	// The library keeps the snapshot reading order (chapters, then outline).
	status, body := env.learner.Do(t, "GET", "/api/content/materials", nil)
	testutil.MustStatus(t, 200, status, body)
	items, _ := body["items"].([]any)
	if len(items) != 38 {
		t.Fatalf("materials endpoint = %d, want 38", len(items))
	}
	titles := make([]string, 0, 3)
	for _, entry := range items[:3] {
		material, _ := entry.(map[string]any)
		titles = append(titles, asString(material["title"]))
	}
	wantTitles := []string{"前言", "第1章 绪论", "第2章 计算机系统基础知识"}
	for i := range wantTitles {
		if titles[i] != wantTitles[i] {
			t.Errorf("material order[%d] = %q, want %q", i, titles[i], wantTitles[i])
		}
	}
}

func TestImportFailuresAreVisibleOnTheJob(t *testing.T) {
	env := newTestEnv(t)
	dir := writeFixture(t, "test-broken", false)
	// Remove one material file and make one link point at a missing section.
	if err := os.Remove(filepath.Join(dir, "materials", "material-b.md")); err != nil {
		t.Fatalf("remove material: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "catalog.json"))
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	patched := string(raw)
	patched = replaceLinkSection(patched)
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), []byte(patched), 0o600); err != nil {
		t.Fatalf("write catalog: %v", err)
	}

	result, err := env.app.Content.ImportSnapshot(t.Context(), dir, "")
	if err != nil {
		t.Fatalf("import should complete with per-item errors, got: %v", err)
	}
	if len(result.Errors) < 2 {
		t.Fatalf("result errors = %v, want missing material and bad link", result.Errors)
	}
	status, body := env.admin.Do(t, "GET", "/api/admin/content/jobs/"+result.JobID, nil)
	testutil.MustStatus(t, 200, status, body)
	job, _ := body["job"].(map[string]any)
	if job["status"] != "partial" {
		t.Errorf("job status = %v, want partial", job["status"])
	}
	items, _ := job["errors"].([]any)
	if len(items) < 2 {
		t.Errorf("job errors = %v, want at least 2 entries", items)
	}
	if got := scalarInt(t, env.db, `select count(*) from materials`); got != 1 {
		t.Errorf("materials = %d, want the one readable material", got)
	}
}

// lessonBlobCount returns the number of distinct lesson bodies, which are
// stored as content-addressed blobs next to the material originals.
func lessonBlobCount(t *testing.T, db *store.DB) int {
	t.Helper()
	return scalarInt(t, db, `select count(distinct body_sha256) from content_lessons`)
}

func replaceLinkSection(catalog string) string {
	// The fixture has exactly one "sectionId" in knowledgeLinks; corrupt it.
	return stringReplaceFirst(catalog, `"sectionId":"section-`, `"sectionId":"section-999999`)
}

func stringReplaceFirst(text, old, new string) string {
	index := -1
	for i := 0; i+len(old) <= len(text); i++ {
		if text[i:i+len(old)] == old {
			index = i
			break
		}
	}
	if index < 0 {
		return text
	}
	return text[:index] + new + text[index+len(old):]
}

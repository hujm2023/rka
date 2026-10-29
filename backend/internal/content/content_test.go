package content_test

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"

	"rka/internal/testutil"
)

// TestContentSnapshotContract verifies original content and indexing: the file matches
// its content hash, answers never ship with the catalog, missing-figure flags
// survive the import, and an older material version stays readable after a new
// one is published.
func TestContentSnapshotContract(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.app.Content.ImportSnapshot(t.Context(), testutil.ContentDir(t), ""); err != nil {
		t.Fatalf("import snapshot: %v", err)
	}

	t.Run("original sha256 matches download", func(t *testing.T) {
		const materialID = "material-7d28d105306c4b"
		var sha, relPath string
		var size int64
		err := env.db.Pool.QueryRow(t.Context(), `
			select b.sha256, b.size, b.rel_path
			from blobs b
			join material_versions v on v.sha256 = b.sha256
			join materials m on m.id = v.material_id
			where m.id = $1 and v.id = m.current_version_id`, materialID).Scan(&sha, &size, &relPath)
		if err != nil {
			t.Fatalf("read blob record: %v", err)
		}
		status, header, body := response(t, env.learner, "/api/content/materials/"+materialID+"/raw")
		if status != http.StatusOK {
			t.Fatalf("raw status = %d", status)
		}
		if !strings.HasPrefix(header.Get("Content-Type"), "text/markdown") {
			t.Errorf("content type = %q", header.Get("Content-Type"))
		}
		sum := sha256.Sum256([]byte(body))
		if got := hex.EncodeToString(sum[:]); got != sha {
			t.Errorf("download sha256 = %s, blob = %s", got, sha)
		}
		if int64(len(body)) != size {
			t.Errorf("download size = %d, blob = %d", len(body), size)
		}
		if relPath != "materials/"+sha+".md" {
			t.Errorf("rel_path = %q", relPath)
		}
	})

	t.Run("answers never ship with the catalog", func(t *testing.T) {
		status, _, raw := response(t, env.learner, "/api/content/catalog")
		if status != http.StatusOK {
			t.Fatalf("catalog status = %d", status)
		}
		for _, forbidden := range []string{`"answer":`, `"analysis":`} {
			if strings.Contains(raw, forbidden) {
				t.Errorf("catalog contains %s", forbidden)
			}
		}
		// The analysis text of a known question must not appear either.
		status, _, raw = response(t, env.learner, "/api/content/questions/mock-2026年5月-模拟卷1-001")
		if status != http.StatusOK {
			t.Fatalf("question status = %d", status)
		}
		if strings.Contains(raw, "互斥信号量用于保证同一时刻只有一个进程访问临界资源") {
			t.Errorf("question detail contains its analysis")
		}
	})

	t.Run("figure missing flag is preserved", func(t *testing.T) {
		want := scalarInt(t, env.db, `select count(*) from questions where figure_missing`)
		if want != 79 {
			t.Errorf("figure_missing questions = %d, want 79 from the snapshot", want)
		}
		status, body := env.learner.Do(t, http.MethodGet, "/api/content/questions?figureMissing=true&limit=5", nil)
		testutil.MustStatus(t, http.StatusOK, status, body)
		items, _ := body["items"].([]any)
		if len(items) == 0 {
			t.Fatalf("no figureMissing questions returned")
		}
		for _, entry := range items {
			question, _ := entry.(map[string]any)
			if question["figureMissing"] != true {
				t.Errorf("figureMissing = %v", question["figureMissing"])
			}
		}
	})

	t.Run("structured figures round trip", func(t *testing.T) {
		status, body := env.learner.Do(t, http.MethodGet, "/api/content/questions/real-2009年下半年-001", nil)
		testutil.MustStatus(t, http.StatusOK, status, body)
		question, _ := body["question"].(map[string]any)
		figure, ok := question["figure"].(map[string]any)
		if !ok || figure["kind"] != "layer-stack" {
			t.Errorf("figure = %v, want the layer-stack payload", question["figure"])
		}
	})

	t.Run("older material version stays readable", func(t *testing.T) {
		v1 := writeFixture(t, "test-v1", false)
		if _, err := env.app.Content.ImportSnapshot(t.Context(), v1, ""); err != nil {
			t.Fatalf("import v1: %v", err)
		}
		v2 := writeFixture(t, "test-v2", true)
		if _, err := env.app.Content.ImportSnapshot(t.Context(), v2, ""); err != nil {
			t.Fatalf("import v2: %v", err)
		}
		status, _, oldBody := response(t, env.learner, "/api/content/materials/material-a/raw?version=1")
		if status != http.StatusOK || !strings.Contains(oldBody, "第一范式要求属性不可再分") {
			t.Fatalf("old version not readable: status=%d", status)
		}
		if strings.Contains(oldBody, "本章补充一段说明") {
			t.Errorf("version 1 returned the new content")
		}
		status, _, currentBody := response(t, env.learner, "/api/content/materials/material-a/raw")
		if status != http.StatusOK || !strings.Contains(currentBody, "本章补充一段说明") {
			t.Errorf("current version not served: status=%d", status)
		}
		// The catalog link is re-bound to the current version's anchors.
		status, body := env.learner.Do(t, http.MethodGet, "/api/content/catalog", nil)
		testutil.MustStatus(t, http.StatusOK, status, body)
		links, _ := body["knowledgeLinks"].(map[string]any)
		list, _ := links["q-1"].([]any)
		if len(list) != 1 {
			t.Fatalf("q-1 links = %v", links["q-1"])
		}
		link, _ := list[0].(map[string]any)
		materials, _ := body["materials"].([]any)
		var currentAnchor string
		for _, entry := range materials {
			material, _ := entry.(map[string]any)
			if material["id"] != "material-a" {
				continue
			}
			sections, _ := material["sections"].([]any)
			for _, sectionEntry := range sections {
				section, _ := sectionEntry.(map[string]any)
				if section["title"] == "1. 关系数据库" {
					currentAnchor, _ = section["id"].(string)
				}
			}
		}
		if currentAnchor == "" || link["sectionId"] != currentAnchor {
			t.Errorf("link sectionId = %v, current anchor = %q", link["sectionId"], currentAnchor)
		}
	})
}

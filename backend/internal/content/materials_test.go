package content_test

import (
	"net/http"
	"strings"
	"testing"

	"rka/internal/testutil"
)

func TestMaterialVersionedReads(t *testing.T) {
	env := newTestEnv(t)
	v1Dir := writeFixture(t, "test-v1", false)
	if _, err := env.app.Content.ImportSnapshot(t.Context(), v1Dir, ""); err != nil {
		t.Fatalf("import v1: %v", err)
	}

	status, body := env.learner.Do(t, http.MethodGet, "/api/content/materials", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("materials = %d, want 2", len(items))
	}

	status, body = env.learner.Do(t, http.MethodGet, "/api/content/materials/material-a", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	material, _ := body["material"].(map[string]any)
	if material["version"] != float64(1) {
		t.Errorf("v1 material version = %v", material["version"])
	}
	if material["versionId"] == "" || material["versionId"] != material["currentVersionId"] {
		t.Errorf("versionId/currentVersionId = %v/%v", material["versionId"], material["currentVersionId"])
	}
	v1Sections, _ := material["sections"].([]any)
	v1Anchor, v1Key := sectionAnchorAndKey(t, v1Sections, "1. 关系数据库")

	// Publish a new version whose headings sit at different offsets.
	v2Dir := writeFixture(t, "test-v2", true)
	if _, err := env.app.Content.ImportSnapshot(t.Context(), v2Dir, ""); err != nil {
		t.Fatalf("import v2: %v", err)
	}
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/materials/material-a", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	material, _ = body["material"].(map[string]any)
	if material["version"] != float64(2) {
		t.Errorf("current material version = %v, want 2", material["version"])
	}
	v2Sections, _ := material["sections"].([]any)
	v2Anchor, v2Key := sectionAnchorAndKey(t, v2Sections, "1. 关系数据库")
	if v2Anchor == v1Anchor {
		t.Errorf("anchor did not change across versions: %q", v2Anchor)
	}
	if v1Key != v2Key {
		t.Errorf("stable key changed across versions: %q vs %q", v1Key, v2Key)
	}

	// Version list keeps both versions and marks the current one.
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/materials/material-a/versions", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	versions, _ := body["items"].([]any)
	if len(versions) != 2 {
		t.Fatalf("versions = %d, want 2", len(versions))
	}
	var versionOneID string
	for _, entry := range versions {
		version, _ := entry.(map[string]any)
		if version["version"] == float64(1) {
			versionOneID, _ = version["id"].(string)
			if version["current"] == true {
				t.Errorf("version 1 should not be current")
			}
		}
		if version["version"] == float64(2) && version["current"] != true {
			t.Errorf("version 2 should be current")
		}
	}
	if versionOneID == "" {
		t.Fatalf("version 1 id missing")
	}

	// Historical original stays downloadable by number and by UUID.
	status, header, raw := response(t, env.learner, "/api/content/materials/material-a/raw?version=1")
	if status != http.StatusOK {
		t.Fatalf("raw v1 status = %d body=%s", status, raw)
	}
	if !strings.HasPrefix(header.Get("Content-Type"), "text/markdown") {
		t.Errorf("raw content type = %q", header.Get("Content-Type"))
	}
	if !strings.Contains(raw, "第一范式要求属性不可再分") || strings.Contains(raw, "本章补充一段说明") {
		t.Errorf("raw v1 does not match the historical version: %s", raw)
	}
	status, _, rawV1ByID := response(t, env.learner, "/api/content/materials/material-a/raw?version="+versionOneID)
	if status != http.StatusOK || rawV1ByID != raw {
		t.Errorf("raw by version id differs: status=%d", status)
	}
	status, _, rawV2 := response(t, env.learner, "/api/content/materials/material-a/raw?version=2")
	if status != http.StatusOK || !strings.Contains(rawV2, "本章补充一段说明") {
		t.Errorf("raw v2 missing new content: status=%d", status)
	}
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/materials/material-a/raw?version=99", nil)
	if status != http.StatusNotFound {
		t.Errorf("unknown version status = %d body=%v", status, body)
	}
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/materials/material-a/raw?version=nonsense", nil)
	if status != http.StatusBadRequest {
		t.Errorf("bad version status = %d body=%v", status, body)
	}

	// The reader payload returns the current body plus its section tree.
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/materials/material-a/content", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if !strings.Contains(asString(body["content"]), "本章补充一段说明") {
		t.Errorf("reader content is not the current version")
	}
	if body["materialVersionId"] == "" {
		t.Errorf("reader payload missing materialVersionId")
	}
	sections, _ := body["sections"].([]any)
	if anchor, _ := sectionAnchorAndKey(t, sections, "1. 关系数据库"); anchor != v2Anchor {
		t.Errorf("reader sections use %q, want current anchor %q", anchor, v2Anchor)
	}
}

func sectionAnchorAndKey(t *testing.T, sections []any, title string) (string, string) {
	t.Helper()
	for _, entry := range sections {
		section, _ := entry.(map[string]any)
		if section["title"] == title {
			anchor, _ := section["id"].(string)
			key, _ := section["key"].(string)
			return anchor, key
		}
	}
	t.Fatalf("section %q not found in %v", title, sections)
	return "", ""
}

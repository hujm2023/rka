package content_test

import (
	"net/http"
	"strings"
	"testing"

	"rka/internal/testutil"
)

func TestSearchFindsMaterialsSectionsAndQuestions(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.app.Content.ImportSnapshot(t.Context(), writeFixture(t, "test-v1", false), ""); err != nil {
		t.Fatalf("import fixture: %v", err)
	}
	_, body := env.learner.Do(t, http.MethodGet, "/api/content/materials/material-a", nil)
	material, _ := body["material"].(map[string]any)
	anchor, _ := sectionAnchorAndKey(t, material["sections"].([]any), "1. 关系数据库")

	status, body := env.learner.Do(t, http.MethodGet, "/api/content/search?q=范式", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items, _ := body["items"].([]any)
	foundSection := false
	for _, entry := range items {
		item, _ := entry.(map[string]any)
		if item["type"] == "section" && item["materialId"] == "material-a" && item["anchor"] == anchor {
			foundSection = true
			if !strings.Contains(asString(item["snippet"]), "范式") {
				t.Errorf("section snippet = %v", item["snippet"])
			}
			if item["sectionKey"] == "" {
				t.Errorf("section hit missing stable key")
			}
		}
	}
	if !foundSection {
		t.Errorf("no section hit for 范式: %v", items)
	}

	// Multi-term queries are ANDed across the section title and body.
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/search?q=%E5%85%B3%E7%B3%BB%20%E8%8C%83%E5%BC%8F", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items, _ = body["items"].([]any)
	multiTerm := false
	for _, entry := range items {
		item, _ := entry.(map[string]any)
		if item["type"] == "section" && item["materialId"] == "material-a" {
			multiTerm = true
		}
	}
	if !multiTerm {
		t.Errorf("multi-term search missed the section: %v", items)
	}

	status, body = env.learner.Do(t, http.MethodGet, "/api/content/search?q=进程", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items, _ = body["items"].([]any)
	foundB := false
	for _, entry := range items {
		item, _ := entry.(map[string]any)
		if item["type"] == "section" && item["materialId"] == "material-b" {
			foundB = true
		}
	}
	if !foundB {
		t.Errorf("no material-b section hit for 进程: %v", items)
	}

	status, body = env.learner.Do(t, http.MethodGet, "/api/content/search?q=数据库", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items, _ = body["items"].([]any)
	var materialHit, questionHit bool
	for _, entry := range items {
		item, _ := entry.(map[string]any)
		switch item["type"] {
		case "material":
			if item["materialId"] == "material-a" && item["anchor"] != "" {
				materialHit = true
			}
		case "question":
			if item["questionId"] == "q-1" && item["revision"] == "rev-1" {
				questionHit = true
			}
		}
	}
	if !materialHit || !questionHit {
		t.Errorf("combined search material/question hits = %v/%v: %v", materialHit, questionHit, items)
	}

	status, body = env.learner.Do(t, http.MethodGet, "/api/content/search", nil)
	if status != http.StatusBadRequest {
		t.Errorf("missing q status = %d body=%v", status, body)
	}
}

func TestAnnotationCRUDIsUserScoped(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.app.Content.ImportSnapshot(t.Context(), writeFixture(t, "test-v1", false), ""); err != nil {
		t.Fatalf("import fixture: %v", err)
	}
	_, body := env.learner.Do(t, http.MethodGet, "/api/content/materials/material-a", nil)
	material, _ := body["material"].(map[string]any)
	anchor, _ := sectionAnchorAndKey(t, material["sections"].([]any), "1. 关系数据库")

	status, body := env.learner.Do(t, http.MethodPost, "/api/content/annotations", map[string]any{
		"materialId": "material-a", "sectionId": anchor,
		"quote": "关系数据库使用范式来减少冗余。", "note": "复习时重点看。",
	})
	testutil.MustStatus(t, http.StatusCreated, status, body)
	annotation, _ := body["annotation"].(map[string]any)
	annotationID, _ := annotation["id"].(string)
	if annotationID == "" {
		t.Fatalf("annotation id missing: %v", body)
	}
	if annotation["materialVersionId"] == "" || annotation["sectionAnchor"] != anchor {
		t.Errorf("annotation = %v", annotation)
	}

	status, body = env.learner.Do(t, http.MethodGet, "/api/content/annotations?materialId=material-a", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("learner annotations = %d, want 1", len(items))
	}

	// Another learner cannot see or delete the annotation.
	status, body = env.other.Do(t, http.MethodGet, "/api/content/annotations", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if items, _ := body["items"].([]any); len(items) != 0 {
		t.Errorf("other learner saw %d annotations", len(items))
	}
	status, body = env.other.Do(t, http.MethodDelete, "/api/content/annotations/"+annotationID, nil)
	if status != http.StatusNotFound {
		t.Errorf("cross-user delete status = %d body=%v", status, body)
	}

	status, body = env.learner.Do(t, http.MethodDelete, "/api/content/annotations/"+annotationID, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/annotations?materialId=material-a", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if items, _ := body["items"].([]any); len(items) != 0 {
		t.Errorf("annotation survived soft delete: %v", items)
	}
	// Soft delete keeps the row for auditability.
	if got := scalarInt(t, env.db, `select count(*) from annotations where deleted_at is not null`); got != 1 {
		t.Errorf("soft deleted rows = %d, want 1", got)
	}

	status, body = env.learner.Do(t, http.MethodPost, "/api/content/annotations", map[string]any{
		"materialId": "material-a", "quote": "  ", "note": "",
	})
	if status != http.StatusBadRequest {
		t.Errorf("empty annotation status = %d body=%v", status, body)
	}
	status, body = env.learner.Do(t, http.MethodPost, "/api/content/annotations", map[string]any{
		"materialId": "missing", "note": "x",
	})
	if status != http.StatusNotFound {
		t.Errorf("missing material status = %d body=%v", status, body)
	}
}

package content_test

import (
	"net/http"
	"strings"
	"testing"

	"rka/internal/testutil"
)

func TestCatalogExcludesAnswersAndCarriesSectionIdentity(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.app.Content.ImportSnapshot(t.Context(), writeFixture(t, "test-v1", false), ""); err != nil {
		t.Fatalf("import fixture: %v", err)
	}

	status, body := env.learner.Do(t, http.MethodGet, "/api/content/catalog", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["version"] != "test-v1" {
		t.Errorf("version = %v", body["version"])
	}
	if body["serverTime"] == "" || body["serverTime"] == nil {
		t.Errorf("serverTime missing")
	}
	questions, _ := body["questions"].([]any)
	if len(questions) != 2 {
		t.Fatalf("questions = %d, want 2", len(questions))
	}
	for _, entry := range questions {
		question, _ := entry.(map[string]any)
		if _, ok := question["answer"]; ok {
			t.Errorf("catalog question leaked answer: %v", question["id"])
		}
		if _, ok := question["analysis"]; ok {
			t.Errorf("catalog question leaked analysis: %v", question["id"])
		}
		if question["revision"] == "" {
			t.Errorf("catalog question missing revision: %v", question["id"])
		}
	}
	materials, _ := body["materials"].([]any)
	if len(materials) != 2 {
		t.Fatalf("materials = %d, want 2", len(materials))
	}
	for _, entry := range materials {
		material, _ := entry.(map[string]any)
		sections, _ := material["sections"].([]any)
		if len(sections) == 0 {
			t.Fatalf("material %v has no sections", material["id"])
		}
		for _, sectionEntry := range sections {
			section, _ := sectionEntry.(map[string]any)
			if section["key"] == "" || section["key"] == nil {
				t.Errorf("section missing stable key: %v", section)
			}
			if _, ok := section["path"].([]any); !ok {
				t.Errorf("section path is not an array: %v", section)
			}
			if section["id"] == "" {
				t.Errorf("section missing anchor id: %v", section)
			}
		}
	}
	links, _ := body["knowledgeLinks"].(map[string]any)
	if len(links) != 1 {
		t.Errorf("knowledgeLinks = %v, want one question", links)
	}
	cases, _ := body["cases"].([]any)
	essays, _ := body["essays"].([]any)
	if len(cases) != 1 || len(essays) != 1 {
		t.Errorf("cases/essays = %d/%d", len(cases), len(essays))
	}

	// Defence in depth: the raw response must not contain the answer text.
	_, _, raw := response(t, env.learner, "/api/content/catalog")
	if strings.Contains(raw, "范式化可以减少数据冗余与更新异常") {
		t.Errorf("catalog response contains the analysis text")
	}
	if strings.Contains(raw, `"answer"`) {
		t.Errorf("catalog response contains an answer key")
	}
}

func TestQuestionListPaginationAndDetail(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.app.Content.ImportSnapshot(t.Context(), writeFixture(t, "test-v1", false), ""); err != nil {
		t.Fatalf("import fixture: %v", err)
	}
	status, body := env.learner.Do(t, http.MethodGet, "/api/content/questions?limit=1", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	cursor, _ := body["nextCursor"].(string)
	if cursor == "" {
		t.Fatalf("nextCursor missing")
	}
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/questions?limit=1&cursor="+cursor, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items, _ = body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("second page items = %d", len(items))
	}
	// Filter by module.
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/questions?module=database", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items, _ = body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("module filter items = %d", len(items))
	}

	status, body = env.learner.Do(t, http.MethodGet, "/api/content/questions/q-1", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	question, _ := body["question"].(map[string]any)
	if question["id"] != "q-1" || question["revision"] != "rev-1" {
		t.Errorf("question = %v", question)
	}
	if _, ok := question["answer"]; ok {
		t.Errorf("question detail leaked answer")
	}
	_, _, raw := response(t, env.learner, "/api/content/questions/q-1")
	if strings.Contains(raw, `"answer"`) || strings.Contains(raw, "解析") {
		t.Errorf("question detail response leaks solution fields: %s", raw)
	}
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/questions/missing", nil)
	testutil.MustStatus(t, http.StatusNotFound, status, body)
}

func TestSolutionNeedsSubmittedAttempt(t *testing.T) {
	env := newTestEnv(t)
	if _, err := env.app.Content.ImportSnapshot(t.Context(), writeFixture(t, "test-v1", false), ""); err != nil {
		t.Fatalf("import fixture: %v", err)
	}
	status, body := env.learner.Do(t, http.MethodGet, "/api/content/questions/q-1/solution", nil)
	if status != http.StatusForbidden {
		t.Fatalf("solution before attempt: status %d body=%v", status, body)
	}
	insertAttempt(t, env.db, env.learnerID, "q-1", "rev-1", "B", true)
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/questions/q-1/solution", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["answer"] != "B" || body["answerHidden"] != false {
		t.Errorf("solution = %v", body)
	}
	if !strings.Contains(asString(body["analysis"]), "范式化") {
		t.Errorf("analysis = %v", body["analysis"])
	}
	if rule, _ := body["scoringRule"].(map[string]any); rule["ruleVersion"] != "v1" {
		t.Errorf("scoringRule = %v", body["scoringRule"])
	}

	// Administrators may always read the solution.
	status, body = env.admin.Do(t, http.MethodGet, "/api/content/questions/q-2/solution", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["answer"] != "B" {
		t.Errorf("admin solution = %v", body)
	}

	// While an exam containing the question is active, the answer is hidden
	// even for a learner with a previous attempt.
	insertExamSession(t, env.db, env.learnerID, "q-1")
	status, body = env.learner.Do(t, http.MethodGet, "/api/content/questions/q-1/solution", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["answerHidden"] != true {
		t.Errorf("exam solution not hidden: %v", body)
	}
	if _, ok := body["answer"]; ok {
		t.Errorf("exam solution leaked answer: %v", body)
	}
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}

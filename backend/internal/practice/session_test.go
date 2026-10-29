package practice_test

import (
	"net/http"
	"testing"

	"rka/internal/testutil"
)

func TestSessionCreateResumeConflictAndReplace(t *testing.T) {
	_, server, _, fixture := newTestEnv(t)

	session := createSession(t, server, map[string]any{"mode": "all", "count": 3}, http.StatusCreated)
	sessionID := mustString(t, session["id"])
	if got := len(questionIDList(t, session)); got != 3 {
		t.Fatalf("question count = %d, want 3", got)
	}
	if mustString(t, session["status"]) != "active" {
		t.Fatalf("status = %v, want active", session["status"])
	}
	if mustInt(t, session["version"]) != 1 {
		t.Fatalf("version = %v, want 1", session["version"])
	}
	revisions := mustMap(t, session["revisions"])
	for _, questionID := range questionIDList(t, session) {
		if mustString(t, revisions[questionID]) != fixture.revision(questionID) {
			t.Fatalf("revision for %s = %v, want %s", questionID, revisions[questionID], fixture.revision(questionID))
		}
	}

	// Re-opening the same range resumes the same server-side session.
	resumed := createSession(t, server, map[string]any{"mode": "all", "count": 3}, http.StatusOK)
	if mustString(t, resumed["id"]) != sessionID {
		t.Fatalf("resumed session id = %v, want %s", resumed["id"], sessionID)
	}

	// A different range without replace is an explicit conflict carrying the
	// current session.
	status, body := server.Do(t, http.MethodPost, "/api/practice/sessions", map[string]any{
		"mode": "chapter", "scope": map[string]any{"materialId": fixture.MaterialOne.ID}, "count": 1,
	})
	testutil.MustStatus(t, http.StatusConflict, status, body)
	apiErr := errorFrom(t, body)
	if mustString(t, apiErr["code"]) != "active_session" {
		t.Fatalf("error code = %v, want active_session", apiErr["code"])
	}
	details := mustMap(t, apiErr["details"])
	if mustString(t, mustMap(t, details["session"])["id"]) != sessionID {
		t.Fatalf("active_session details session id mismatch: %v", details)
	}

	// replace:true abandons the old session and creates the new one.
	replaced := createSession(t, server, map[string]any{
		"mode": "chapter", "scope": map[string]any{"materialId": fixture.MaterialOne.ID}, "count": 10, "replace": true,
	}, http.StatusCreated)
	newID := mustString(t, replaced["id"])
	if newID == sessionID {
		t.Fatalf("replace returned the old session")
	}
	if mustString(t, replaced["mode"]) != "chapter" {
		t.Fatalf("mode = %v, want chapter", replaced["mode"])
	}
	if mustString(t, mustMap(t, replaced["scope"])["materialId"]) != fixture.MaterialOne.ID {
		t.Fatalf("scope.materialId mismatch: %v", replaced["scope"])
	}
	if got := len(questionIDList(t, replaced)); got != 2 {
		t.Fatalf("chapter question count = %d, want 2", got)
	}

	status, body = server.Do(t, http.MethodGet, "/api/practice/sessions/"+sessionID, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if mustString(t, sessionFrom(t, body)["status"]) != "abandoned" {
		t.Fatalf("old session status = %v, want abandoned", sessionFrom(t, body)["status"])
	}

	status, body = server.Do(t, http.MethodGet, "/api/practice/sessions/active", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if mustString(t, sessionFrom(t, body)["id"]) != newID {
		t.Fatalf("active session = %v, want %s", body["session"], newID)
	}
}

func TestSessionScopeValidation(t *testing.T) {
	_, server, _, _ := newTestEnv(t)

	cases := []struct {
		name   string
		body   map[string]any
		status int
		code   string
	}{
		{name: "missing mode", body: map[string]any{"mode": ""}, status: http.StatusBadRequest, code: "mode_required"},
		{name: "invalid mode", body: map[string]any{"mode": "unknown"}, status: http.StatusBadRequest, code: "invalid_mode"},
		{name: "chapter without material", body: map[string]any{"mode": "chapter"}, status: http.StatusBadRequest, code: "material_required"},
		{name: "exam without term", body: map[string]any{"mode": "exam"}, status: http.StatusBadRequest, code: "term_required"},
		{name: "invalid source", body: map[string]any{"mode": "all", "scope": map[string]any{"source": "other"}}, status: http.StatusBadRequest, code: "invalid_source"},
		{name: "section without material", body: map[string]any{"mode": "all", "scope": map[string]any{"sectionId": "section-1"}}, status: http.StatusBadRequest, code: "section_requires_material"},
		{name: "invalid count", body: map[string]any{"mode": "all", "count": -1}, status: http.StatusBadRequest, code: "invalid_count"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, body := server.Do(t, http.MethodPost, "/api/practice/sessions", testCase.body)
			testutil.MustStatus(t, testCase.status, status, body)
			if code := mustString(t, errorFrom(t, body)["code"]); code != testCase.code {
				t.Fatalf("code = %s, want %s", code, testCase.code)
			}
		})
	}

	status, body := server.Do(t, http.MethodGet, "/api/practice/sessions/not-a-uuid", nil)
	testutil.MustStatus(t, http.StatusNotFound, status, body)

	// A scope that filters to zero questions is a clear 400, not an empty
	// session.
	status, body = server.Do(t, http.MethodPost, "/api/practice/sessions", map[string]any{
		"mode": "all", "scope": map[string]any{"module": "no-such-module"},
	})
	testutil.MustStatus(t, http.StatusBadRequest, status, body)
	if code := mustString(t, errorFrom(t, body)["code"]); code != "no_questions" {
		t.Fatalf("code = %s, want no_questions", code)
	}
}

func TestSelectionFilters(t *testing.T) {
	_, server, _, fixture := newTestEnv(t)

	t.Run("chapter by material", func(t *testing.T) {
		session := createSession(t, server, map[string]any{
			"mode": "chapter", "scope": map[string]any{"materialId": fixture.MaterialOne.ID}, "count": 10,
		}, http.StatusCreated)
		ids := questionIDList(t, session)
		if len(ids) != 2 || ids[0] != questionA || ids[1] != questionB {
			t.Fatalf("chapter queue = %v, want [q-a q-b]", ids)
		}
		// Finish so the next scenario can start.
		status, body := server.Do(t, http.MethodPost, "/api/practice/sessions/"+mustString(t, session["id"])+"/finish", nil)
		testutil.MustStatus(t, http.StatusOK, status, body)
	})

	t.Run("chapter by section anchor", func(t *testing.T) {
		session := createSession(t, server, map[string]any{
			"mode":  "chapter",
			"scope": map[string]any{"materialId": fixture.MaterialOne.ID, "sectionId": "section-1"},
			"count": 10,
		}, http.StatusCreated)
		ids := questionIDList(t, session)
		if len(ids) != 1 || ids[0] != questionA {
			t.Fatalf("section queue = %v, want [q-a]", ids)
		}
		status, body := server.Do(t, http.MethodPost, "/api/practice/sessions/"+mustString(t, session["id"])+"/finish", nil)
		testutil.MustStatus(t, http.StatusOK, status, body)
	})

	t.Run("module and source filters", func(t *testing.T) {
		session := createSession(t, server, map[string]any{
			"mode": "all", "scope": map[string]any{"module": "architecture"}, "count": 10,
		}, http.StatusCreated)
		ids := questionIDList(t, session)
		if len(ids) != 2 || ids[0] != questionC || ids[1] != questionD {
			t.Fatalf("module queue = %v, want [q-c q-d]", ids)
		}
		status, body := server.Do(t, http.MethodPost, "/api/practice/sessions/"+mustString(t, session["id"])+"/finish", nil)
		testutil.MustStatus(t, http.StatusOK, status, body)

		session = createSession(t, server, map[string]any{
			"mode": "all", "scope": map[string]any{"source": "mock"}, "count": 10,
		}, http.StatusCreated)
		ids = questionIDList(t, session)
		if len(ids) != 1 || ids[0] != questionC {
			t.Fatalf("source queue = %v, want [q-c]", ids)
		}
		status, body = server.Do(t, http.MethodPost, "/api/practice/sessions/"+mustString(t, session["id"])+"/finish", nil)
		testutil.MustStatus(t, http.StatusOK, status, body)
	})

	t.Run("missing figure and withdrawn questions stay out", func(t *testing.T) {
		session := createSession(t, server, map[string]any{"mode": "all", "count": 10}, http.StatusCreated)
		ids := questionIDList(t, session)
		if len(ids) != 4 {
			t.Fatalf("all queue = %v, want 4 questions", ids)
		}
		for _, id := range ids {
			if id == questionMissing || id == questionWithdrawn {
				t.Fatalf("queue includes excluded question %s: %v", id, ids)
			}
		}
		status, body := server.Do(t, http.MethodPost, "/api/practice/sessions/"+mustString(t, session["id"])+"/finish", nil)
		testutil.MustStatus(t, http.StatusOK, status, body)
	})

	t.Run("query and includeMissing", func(t *testing.T) {
		session := createSession(t, server, map[string]any{
			"mode": "all", "scope": map[string]any{"query": "q-c"}, "count": 10,
		}, http.StatusCreated)
		ids := questionIDList(t, session)
		if len(ids) != 1 || ids[0] != questionC {
			t.Fatalf("query queue = %v, want [q-c]", ids)
		}
		status, body := server.Do(t, http.MethodPost, "/api/practice/sessions/"+mustString(t, session["id"])+"/finish", nil)
		testutil.MustStatus(t, http.StatusOK, status, body)

		session = createSession(t, server, map[string]any{
			"mode": "all", "scope": map[string]any{"includeMissing": true}, "count": 10,
		}, http.StatusCreated)
		ids = questionIDList(t, session)
		if len(ids) != 5 {
			t.Fatalf("includeMissing queue = %v, want 5 questions", ids)
		}
		found := false
		for _, id := range ids {
			if id == questionMissing {
				found = true
			}
		}
		if !found {
			t.Fatalf("includeMissing queue does not include the missing-figure question: %v", ids)
		}
		status, body = server.Do(t, http.MethodPost, "/api/practice/sessions/"+mustString(t, session["id"])+"/finish", nil)
		testutil.MustStatus(t, http.StatusOK, status, body)
	})

	t.Run("favorites", func(t *testing.T) {
		status, body := server.Do(t, http.MethodPut, "/api/practice/marks/"+questionC, map[string]any{
			"favorite": true, "version": 0,
		})
		testutil.MustStatus(t, http.StatusOK, status, body)
		session := createSession(t, server, map[string]any{"mode": "favorites", "count": 10}, http.StatusCreated)
		ids := questionIDList(t, session)
		if len(ids) != 1 || ids[0] != questionC {
			t.Fatalf("favorites queue = %v, want [q-c]", ids)
		}
		status, body = server.Do(t, http.MethodPost, "/api/practice/sessions/"+mustString(t, session["id"])+"/finish", nil)
		testutil.MustStatus(t, http.StatusOK, status, body)
	})

	t.Run("wrong book queue", func(t *testing.T) {
		session := createSession(t, server, map[string]any{"mode": "all", "count": 10}, http.StatusCreated)
		sessionID := mustString(t, session["id"])
		submitAnswer(t, server, sessionID, answerPayload("evt-wrong-a", questionA, "A"), http.StatusOK)
		status, body := server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/finish", nil)
		testutil.MustStatus(t, http.StatusOK, status, body)

		wrongQueue := createSession(t, server, map[string]any{"mode": "wrong", "count": 10}, http.StatusCreated)
		ids := questionIDList(t, wrongQueue)
		if len(ids) != 1 || ids[0] != questionA {
			t.Fatalf("wrong queue = %v, want [q-a]", ids)
		}
	})
}

func TestParentSectionIncludesDescendants(t *testing.T) {
	_, server, db, fixture := newTestEnv(t)
	if _, err := db.Pool.Exec(t.Context(), `insert into material_sections(material_version_id,anchor,stable_key,title,level,start_offset) values($1::uuid,'parent','parent-key','Parent',1,0)`, fixture.MaterialOne.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `update material_sections set path=array['parent'] where material_version_id=$1::uuid and anchor<>'parent'`, fixture.MaterialOne.Version); err != nil {
		t.Fatal(err)
	}
	session := createSession(t, server, map[string]any{"mode": "chapter", "scope": map[string]any{"materialId": fixture.MaterialOne.ID, "sectionId": "parent"}, "count": 10}, http.StatusCreated)
	if len(questionIDList(t, session)) != 2 {
		t.Fatalf("parent did not include descendants: %v", session)
	}
}

func TestPracticeSearchTreatsPercentAsLiteral(t *testing.T) {
	_, server, db, _ := newTestEnv(t)
	if _, err := db.Pool.Exec(t.Context(), `update question_versions set stem='计算 50% 的利用率' where question_id=$1`, questionA); err != nil {
		t.Fatal(err)
	}
	session := createSession(t, server, map[string]any{"mode": "all", "scope": map[string]any{"query": "%"}, "count": 10}, http.StatusCreated)
	ids := questionIDList(t, session)
	if len(ids) != 1 || ids[0] != questionA {
		t.Fatalf("literal search returned %v", ids)
	}
}

package practice_test

import (
	"context"
	"net/http"
	"testing"

	"rka/internal/testutil"
)

func TestCrossDeviceResume(t *testing.T) {
	application, server, _, fixture := newTestEnv(t)
	session := createSession(t, server, map[string]any{"mode": "all", "count": 3}, http.StatusCreated)
	sessionID := mustString(t, session["id"])
	first := questionIDList(t, session)[0]
	submitAnswer(t, server, sessionID, answerPayload("evt-device-1", first, fixture.answer(first)), http.StatusOK)

	// A second browser (separate cookie jar) logs into the same account and
	// recovers the server-confirmed session.
	_, secondServer, _ := testutil.NewAppFor(t, application)
	testutil.Login(t, secondServer, learnerEmail, learnerPassword)

	status, body := secondServer.Do(t, http.MethodGet, "/api/practice/sessions/active", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	resumed := sessionFrom(t, body)
	if mustString(t, resumed["id"]) != sessionID {
		t.Fatalf("second device active session = %v, want %s", resumed["id"], sessionID)
	}
	answers := mustMap(t, resumed["answers"])
	if len(answers) != 1 || !mustMap(t, answers[first])["submitted"].(bool) {
		t.Fatalf("second device did not recover submitted answer: %v", answers)
	}

	status, body = secondServer.Do(t, http.MethodPatch, "/api/practice/sessions/"+sessionID, map[string]any{
		"index": 2, "elapsedSeconds": 90, "version": 1,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)

	status, body = server.Do(t, http.MethodGet, "/api/practice/sessions/"+sessionID, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	firstDevice := sessionFrom(t, body)
	if mustInt(t, firstDevice["index"]) != 2 || mustInt(t, firstDevice["elapsedSeconds"]) != 90 {
		t.Fatalf("server did not keep second-device progress: %v", firstDevice)
	}
	if mustInt(t, firstDevice["version"]) != 2 {
		t.Fatalf("session version = %v, want 2", firstDevice["version"])
	}
}

func TestVersionConflictsAndMarks(t *testing.T) {
	_, server, _, _ := newTestEnv(t)
	session := createSession(t, server, map[string]any{"mode": "all", "count": 3}, http.StatusCreated)
	sessionID := mustString(t, session["id"])

	status, body := server.Do(t, http.MethodPatch, "/api/practice/sessions/"+sessionID, map[string]any{
		"index": 1, "elapsedSeconds": 30, "version": 1,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	patched := sessionFrom(t, body)
	if mustInt(t, patched["index"]) != 1 || mustInt(t, patched["version"]) != 2 {
		t.Fatalf("patched session = %v", patched)
	}

	status, body = server.Do(t, http.MethodPatch, "/api/practice/sessions/"+sessionID, map[string]any{
		"index": 0, "version": 1,
	})
	testutil.MustStatus(t, http.StatusConflict, status, body)
	apiErr := errorFrom(t, body)
	if mustString(t, apiErr["code"]) != "conflict" {
		t.Fatalf("session patch conflict code = %v", apiErr["code"])
	}
	details := mustMap(t, apiErr["details"])
	if mustInt(t, details["serverVersion"]) != 2 {
		t.Fatalf("serverVersion = %v, want 2", details["serverVersion"])
	}
	serverValues := mustMap(t, details["serverValues"])
	if mustInt(t, serverValues["index"]) != 1 || mustInt(t, serverValues["elapsedSeconds"]) != 30 {
		t.Fatalf("serverValues = %v", serverValues)
	}

	status, body = server.Do(t, http.MethodPatch, "/api/practice/sessions/"+sessionID, map[string]any{
		"index": 99, "version": 2,
	})
	testutil.MustStatus(t, http.StatusBadRequest, status, body)
	if code := mustString(t, errorFrom(t, body)["code"]); code != "invalid_index" {
		t.Fatalf("invalid index code = %s", code)
	}

	// Marks use optimistic concurrency and keep server values on conflict.
	status, body = server.Do(t, http.MethodPut, "/api/practice/marks/"+questionA, map[string]any{
		"favorite": true, "note": "重点复习", "version": 0,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	mark := mustMap(t, body["mark"])
	if mustString(t, mark["id"]) != questionA || mustString(t, mark["questionId"]) != questionA {
		t.Fatalf("mark identifiers = %v", mark)
	}
	if mustInt(t, mark["version"]) != 1 || mark["favorite"] != true || mustString(t, mark["note"]) != "重点复习" {
		t.Fatalf("created mark = %v", mark)
	}

	status, body = server.Do(t, http.MethodPut, "/api/practice/marks/"+questionA, map[string]any{
		"uncertain": true, "version": 1,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	mark = mustMap(t, body["mark"])
	if mustInt(t, mark["version"]) != 2 || mark["favorite"] != true || mark["uncertain"] != true {
		t.Fatalf("updated mark = %v", mark)
	}

	status, body = server.Do(t, http.MethodPut, "/api/practice/marks/"+questionA, map[string]any{
		"favorite": false, "version": 1,
	})
	testutil.MustStatus(t, http.StatusConflict, status, body)
	details = mustMap(t, errorFrom(t, body)["details"])
	if mustInt(t, details["serverVersion"]) != 2 {
		t.Fatalf("mark serverVersion = %v, want 2", details["serverVersion"])
	}
	if serverValues = mustMap(t, details["serverValues"]); serverValues["favorite"] != true {
		t.Fatalf("mark serverValues = %v", serverValues)
	}

	status, body = server.Do(t, http.MethodGet, "/api/practice/marks", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	marks := mustList(t, body["items"])
	if len(marks) != 1 {
		t.Fatalf("marks = %d, want 1", len(marks))
	}
	stored := mustMap(t, marks[0])
	if mustString(t, stored["questionId"]) != questionA || stored["favorite"] != true || stored["uncertain"] != true {
		t.Fatalf("stored mark = %v", stored)
	}

	status, body = server.Do(t, http.MethodPut, "/api/practice/marks/no-such-question", map[string]any{
		"favorite": true, "version": 0,
	})
	testutil.MustStatus(t, http.StatusNotFound, status, body)

	status, body = server.Do(t, http.MethodPut, "/api/practice/marks/"+questionA, map[string]any{"favorite": true})
	testutil.MustStatus(t, http.StatusBadRequest, status, body)
	if code := mustString(t, errorFrom(t, body)["code"]); code != "version_required" {
		t.Fatalf("missing version code = %s", code)
	}
}

func TestFinishPracticeSession(t *testing.T) {
	_, server, _, fixture := newTestEnv(t)
	session := createSession(t, server, map[string]any{"mode": "all", "count": 2}, http.StatusCreated)
	sessionID := mustString(t, session["id"])
	ids := questionIDList(t, session)
	submitAnswer(t, server, sessionID, answerPayload("evt-finish-1", ids[0], fixture.answer(ids[0])), http.StatusOK)
	wrong := "A"
	if fixture.answer(ids[1]) == "A" {
		wrong = "B"
	}
	submitAnswer(t, server, sessionID, answerPayload("evt-finish-2", ids[1], wrong), http.StatusOK)

	status, body := server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/finish", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	finished := sessionFrom(t, body)
	if mustString(t, finished["status"]) != "finished" {
		t.Fatalf("status = %v", finished["status"])
	}
	if mustInt(t, finished["score"]) != 1 || mustInt(t, finished["maxScore"]) != 2 {
		t.Fatalf("practice score = %v/%v, want 1/2", finished["score"], finished["maxScore"])
	}
	if finished["submittedAt"] == nil {
		t.Fatalf("finished session has no submittedAt")
	}

	// Finish is idempotent.
	status, body = server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/finish", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if mustString(t, sessionFrom(t, body)["status"]) != "finished" {
		t.Fatalf("second finish changed status")
	}

	// A finished session rejects new answers and patches.
	status, body = server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/answers",
		answerPayload("evt-finish-3", ids[0], fixture.answer(ids[0])))
	testutil.MustStatus(t, http.StatusConflict, status, body)
	if code := mustString(t, errorFrom(t, body)["code"]); code != "session_finished" {
		t.Fatalf("finished answer code = %s", code)
	}
	status, body = server.Do(t, http.MethodPatch, "/api/practice/sessions/"+sessionID, map[string]any{
		"index": 0, "version": mustInt(t, finished["version"]),
	})
	testutil.MustStatus(t, http.StatusConflict, status, body)
	if code := mustString(t, errorFrom(t, body)["code"]); code != "session_finished" {
		t.Fatalf("finished patch code = %s", code)
	}

	// Formal exams must be submitted, not finished.
	exam := createSession(t, server, map[string]any{
		"mode": "exam", "scope": map[string]any{"term": termExam},
	}, http.StatusCreated)
	status, body = server.Do(t, http.MethodPost, "/api/practice/sessions/"+mustString(t, exam["id"])+"/finish", nil)
	testutil.MustStatus(t, http.StatusBadRequest, status, body)
	if code := mustString(t, errorFrom(t, body)["code"]); code != "exam_requires_submit" {
		t.Fatalf("exam finish code = %s", code)
	}
}

func TestAttemptsHistoryPagination(t *testing.T) {
	_, server, _, fixture := newTestEnv(t)
	session := createSession(t, server, map[string]any{"mode": "all", "count": 2}, http.StatusCreated)
	sessionID := mustString(t, session["id"])
	questionID := questionIDList(t, session)[0]
	submitAnswer(t, server, sessionID, answerPayload("evt-hist-1", questionID, fixture.answer(questionID)), http.StatusOK)
	submitAnswer(t, server, sessionID, answerPayload("evt-hist-2", questionID, "A"), http.StatusOK)

	status, body := server.Do(t, http.MethodGet, "/api/practice/attempts?questionId="+questionID, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items := mustList(t, body["items"])
	if len(items) != 2 {
		t.Fatalf("attempt items = %d, want 2", len(items))
	}
	if mustString(t, mustMap(t, items[0])["id"]) != "evt-hist-2" {
		t.Fatalf("attempt order = %v, want newest first", items[0])
	}

	status, body = server.Do(t, http.MethodGet, "/api/practice/attempts?limit=1", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if len(mustList(t, body["items"])) != 1 {
		t.Fatalf("first page size != 1")
	}
	cursor := mustString(t, body["nextCursor"])
	if cursor == "" {
		t.Fatalf("missing nextCursor")
	}
	status, body = server.Do(t, http.MethodGet, "/api/practice/attempts?limit=1&cursor="+cursor, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if len(mustList(t, body["items"])) != 1 {
		t.Fatalf("second page size != 1")
	}
	if mustString(t, body["nextCursor"]) != "" {
		t.Fatalf("unexpected extra page")
	}
}

func TestWrongBookReviewDueAndStats(t *testing.T) {
	_, server, db, fixture := newTestEnv(t)
	session := createSession(t, server, map[string]any{"mode": "all", "count": 10}, http.StatusCreated)
	sessionID := mustString(t, session["id"])

	submitAnswer(t, server, sessionID, answerPayload("evt-wb-1", questionA, "A"), http.StatusOK)

	status, body := server.Do(t, http.MethodGet, "/api/practice/wrong-book", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	wrongItems := mustList(t, body["items"])
	if len(wrongItems) != 1 {
		t.Fatalf("wrong book = %d, want 1", len(wrongItems))
	}
	wrongItem := mustMap(t, wrongItems[0])
	if mustString(t, wrongItem["questionId"]) != questionA {
		t.Fatalf("wrong book question = %v", wrongItem["questionId"])
	}
	if mustInt(t, wrongItem["wrongCount"]) != 1 {
		t.Fatalf("wrongCount = %v", wrongItem["wrongCount"])
	}

	status, body = server.Do(t, http.MethodGet, "/api/practice/review-due", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if len(mustList(t, body["items"])) != 0 {
		t.Fatalf("review-due should be empty immediately after a wrong answer")
	}

	// The learning rule sends a wrong answer back after one day.
	if _, err := db.Pool.Exec(context.Background(), `update attempts set answered_at = now() - interval '2 days' where id = 'evt-wb-1'`); err != nil {
		t.Fatalf("backdate attempt: %v", err)
	}
	status, body = server.Do(t, http.MethodGet, "/api/practice/review-due", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	dueItems := mustList(t, body["items"])
	if len(dueItems) != 1 {
		t.Fatalf("review-due = %d, want 1", len(dueItems))
	}
	due := mustMap(t, dueItems[0])
	if mustString(t, due["questionId"]) != questionA || mustInt(t, due["intervalDays"]) != 1 {
		t.Fatalf("review-due item = %v", due)
	}

	status, body = server.Do(t, http.MethodGet, "/api/practice/stats", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	totals := mustMap(t, body["totals"])
	if mustInt(t, totals["attempts"]) != 1 || mustInt(t, totals["correct"]) != 0 {
		t.Fatalf("totals = %v", totals)
	}
	if mustFloat(t, totals["accuracy"]) != 0 {
		t.Fatalf("accuracy = %v, want 0", totals["accuracy"])
	}
	if mustInt(t, totals["wrongQuestions"]) != 1 || mustInt(t, totals["dueQuestions"]) != 1 {
		t.Fatalf("wrong/due counts = %v/%v", totals["wrongQuestions"], totals["dueQuestions"])
	}
	modules := mustList(t, body["modules"])
	if len(modules) == 0 {
		t.Fatalf("stats has no modules")
	}

	// A later independent correct answer replaces the wrong verdict in the
	// wrong-book while the original fact stays in the history.
	submitAnswer(t, server, sessionID, answerPayload("evt-wb-2", questionA, fixture.answer(questionA)), http.StatusOK)
	status, body = server.Do(t, http.MethodGet, "/api/practice/wrong-book", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if len(mustList(t, body["items"])) != 0 {
		t.Fatalf("wrong book should exclude a corrected question")
	}
	status, body = server.Do(t, http.MethodGet, "/api/practice/review-due", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if len(mustList(t, body["items"])) != 0 {
		t.Fatalf("correct answer should not be due yet")
	}
	status, body = server.Do(t, http.MethodGet, "/api/practice/stats", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	totals = mustMap(t, body["totals"])
	if mustInt(t, totals["attempts"]) != 2 || mustInt(t, totals["correct"]) != 1 {
		t.Fatalf("totals after correction = %v", totals)
	}
	if mustFloat(t, totals["accuracy"]) != 0.5 || mustInt(t, totals["accuracyPercent"]) != 50 {
		t.Fatalf("accuracy after correction = %v/%v", totals["accuracy"], totals["accuracyPercent"])
	}

	// Assisted passes come back after two days.
	submitAnswer(t, server, sessionID, map[string]any{
		"id": "evt-wb-3", "questionId": questionB, "selected": fixture.answer(questionB),
		"assisted": true, "durationMs": 1000,
	}, http.StatusOK)
	if _, err := db.Pool.Exec(context.Background(), `update attempts set answered_at = now() - interval '3 days' where id = 'evt-wb-3'`); err != nil {
		t.Fatalf("backdate assisted attempt: %v", err)
	}
	status, body = server.Do(t, http.MethodGet, "/api/practice/review-due", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	dueItems = mustList(t, body["items"])
	found := false
	for _, raw := range dueItems {
		item := mustMap(t, raw)
		if mustString(t, item["questionId"]) == questionB {
			found = true
			if mustInt(t, item["intervalDays"]) != 2 {
				t.Fatalf("assisted interval = %v, want 2", item["intervalDays"])
			}
		}
	}
	if !found {
		t.Fatalf("assisted pass not due after 3 days: %v", dueItems)
	}
	status, body = server.Do(t, http.MethodGet, "/api/practice/stats", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	totals = mustMap(t, body["totals"])
	if mustInt(t, totals["assistedCorrect"]) != 1 || mustInt(t, totals["independentCorrect"]) != 1 {
		t.Fatalf("assisted/independent counts = %v/%v", totals["assistedCorrect"], totals["independentCorrect"])
	}
}

func TestOwnershipIsolationAndAuthentication(t *testing.T) {
	application, server, _, fixture := newTestEnv(t)
	session := createSession(t, server, map[string]any{"mode": "all", "count": 2}, http.StatusCreated)
	sessionID := mustString(t, session["id"])
	submitAnswer(t, server, sessionID, answerPayload("evt-owner-1", questionA, fixture.answer(questionA)), http.StatusOK)

	// Another account cannot see or touch the session.
	_, otherServer, _ := testutil.NewAppFor(t, application)
	testutil.CreateUser(t, application, "other@example.com", learnerPassword, "learner")
	testutil.Login(t, otherServer, "other@example.com", learnerPassword)

	status, body := otherServer.Do(t, http.MethodGet, "/api/practice/sessions/"+sessionID, nil)
	testutil.MustStatus(t, http.StatusNotFound, status, body)
	status, body = otherServer.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/answers",
		answerPayload("evt-owner-2", questionA, "A"))
	testutil.MustStatus(t, http.StatusNotFound, status, body)
	status, body = otherServer.Do(t, http.MethodGet, "/api/practice/sessions/active", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["session"] != nil {
		t.Fatalf("other user sees an active session: %v", body["session"])
	}
	status, body = otherServer.Do(t, http.MethodGet, "/api/practice/attempts", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if len(mustList(t, body["items"])) != 0 {
		t.Fatalf("other user sees attempts")
	}
	status, body = otherServer.Do(t, http.MethodGet, "/api/practice/wrong-book", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if len(mustList(t, body["items"])) != 0 {
		t.Fatalf("other user sees wrong book")
	}

	// Anonymous requests get the standard 401 envelope.
	_, anonymousServer, _ := testutil.NewAppFor(t, application)
	status, body = anonymousServer.Do(t, http.MethodGet, "/api/practice/sessions/active", nil)
	testutil.MustStatus(t, http.StatusUnauthorized, status, body)
	if code := mustString(t, errorFrom(t, body)["code"]); code != "unauthorized" {
		t.Fatalf("anonymous code = %s", code)
	}
}

func TestReviewDueDateOrdering(t *testing.T) {
	_, server, db, _ := newTestEnv(t)
	session := createSession(t, server, map[string]any{"mode": "all", "count": 10}, http.StatusCreated)
	sessionID := mustString(t, session["id"])
	submitAnswer(t, server, sessionID, answerPayload("evt-due-1", questionA, "A"), http.StatusOK)
	submitAnswer(t, server, sessionID, answerPayload("evt-due-2", questionB, "A"), http.StatusOK)
	if _, err := db.Pool.Exec(context.Background(), `
		update attempts set answered_at = case id when 'evt-due-1' then now() - interval '5 days' else now() - interval '3 days' end
		where id in ('evt-due-1', 'evt-due-2')`); err != nil {
		t.Fatalf("backdate attempts: %v", err)
	}
	status, body := server.Do(t, http.MethodGet, "/api/practice/review-due", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items := mustList(t, body["items"])
	if len(items) != 2 {
		t.Fatalf("review-due = %d, want 2", len(items))
	}
	if mustString(t, mustMap(t, items[0])["questionId"]) != questionA {
		t.Fatalf("review-due order = %v, want oldest first", items)
	}
}

func TestReviewModeQueue(t *testing.T) {
	_, server, db, _ := newTestEnv(t)
	session := createSession(t, server, map[string]any{"mode": "all", "count": 10}, http.StatusCreated)
	sessionID := mustString(t, session["id"])
	submitAnswer(t, server, sessionID, answerPayload("evt-review-1", questionA, "A"), http.StatusOK)
	if _, err := db.Pool.Exec(context.Background(), `update attempts set answered_at = now() - interval '2 days' where id = 'evt-review-1'`); err != nil {
		t.Fatalf("backdate attempt: %v", err)
	}
	status, body := server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/finish", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)

	reviewSession := createSession(t, server, map[string]any{"mode": "review", "count": 10}, http.StatusCreated)
	ids := questionIDList(t, reviewSession)
	if len(ids) != 1 || ids[0] != questionA {
		t.Fatalf("review queue = %v, want [q-a]", ids)
	}
}

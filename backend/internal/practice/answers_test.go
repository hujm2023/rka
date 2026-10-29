package practice_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"rka/internal/testutil"
)

func TestAnswerIdempotencyAndFactAppend(t *testing.T) {
	_, server, db, fixture := newTestEnv(t)
	session := createSession(t, server, map[string]any{"mode": "all", "count": 2}, http.StatusCreated)
	sessionID := mustString(t, session["id"])
	ids := questionIDList(t, session)
	questionID := ids[0]

	first := submitAnswer(t, server, sessionID, answerPayload("evt-1", questionID, fixture.answer(questionID)), http.StatusOK)
	if correct, _ := first["correct"].(bool); !correct {
		t.Fatalf("first answer correct = %v, want true", first["correct"])
	}
	if revealed, _ := first["revealed"].(bool); !revealed {
		t.Fatalf("practice answer revealed = %v, want true", first["revealed"])
	}
	solution := mustMap(t, first["solution"])
	if mustString(t, solution["answer"]) != fixture.answer(questionID) {
		t.Fatalf("solution answer = %v, want %s", solution["answer"], fixture.answer(questionID))
	}
	if attempt := mustMap(t, first["attempt"]); mustString(t, attempt["id"]) != "evt-1" {
		t.Fatalf("attempt id = %v, want evt-1", attempt["id"])
	}
	answerState := mustMap(t, mustMap(t, mustMap(t, first["session"])["answers"])[questionID])
	if answerState["solution"] == nil {
		t.Fatalf("practice session answer is missing the revealed solution")
	}
	if answerState["revealed"] != true {
		t.Fatalf("practice session answer revealed = %v, want true", answerState["revealed"])
	}
	if mustString(t, mustMap(t, answerState["solution"])["analysis"]) == "" {
		t.Fatalf("practice session answer solution has no analysis")
	}
	if got := countRows(t, db, `select count(*) from attempts where user_id = (select id from users where email = $1)`, learnerEmail); got != 1 {
		t.Fatalf("attempt rows = %d, want 1", got)
	}

	// A reload of the session from the server still carries the revealed
	// solution for practice mode.
	status, body := server.Do(t, http.MethodGet, "/api/practice/sessions/"+sessionID, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	reloaded := mustMap(t, mustMap(t, sessionFrom(t, body)["answers"])[questionID])
	if reloaded["revealed"] != true || reloaded["solution"] == nil {
		t.Fatalf("reloaded practice answer = %v", reloaded)
	}
	if mustString(t, mustMap(t, reloaded["solution"])["answer"]) != fixture.answer(questionID) {
		t.Fatalf("reloaded solution = %v", reloaded["solution"])
	}

	// Retrying the same client event must not insert a second fact or score it
	// again.
	retry := submitAnswer(t, server, sessionID, answerPayload("evt-1", questionID, fixture.answer(questionID)), http.StatusOK)
	if got := countRows(t, db, `select count(*) from attempts where id = 'evt-1'`); got != 1 {
		t.Fatalf("attempt rows after retry = %d, want 1", got)
	}
	if attempt := mustMap(t, retry["attempt"]); mustString(t, attempt["id"]) != "evt-1" {
		t.Fatalf("retry attempt id = %v", attempt["id"])
	}
	if correct, _ := retry["correct"].(bool); !correct {
		t.Fatalf("retry correct = %v, want true", retry["correct"])
	}

	// A different event id appends a new immutable fact; the session answers
	// map shows the latest attempt for the question.
	wrong := submitAnswer(t, server, sessionID, answerPayload("evt-2", questionID, "A"), http.StatusOK)
	if correct, _ := wrong["correct"].(bool); correct {
		t.Fatalf("second answer should be wrong")
	}
	if got := countRows(t, db, `select count(*) from attempts where user_id = (select id from users where email = $1)`, learnerEmail); got != 2 {
		t.Fatalf("attempt rows = %d, want 2 (facts are appended)", got)
	}
	answers := mustMap(t, mustMap(t, wrong["session"])["answers"])
	answerState = mustMap(t, answers[questionID])
	if mustString(t, answerState["attemptId"]) != "evt-2" {
		t.Fatalf("latest attempt id = %v, want evt-2", answerState["attemptId"])
	}
	if mustString(t, answerState["selected"]) != "A" {
		t.Fatalf("latest selected = %v, want A", answerState["selected"])
	}
	if correct, ok := answerState["correct"].(bool); !ok || correct {
		t.Fatalf("latest correct = %v, want false", answerState["correct"])
	}
}

func TestExamHidesAnswersAndDeadlineUsesServerTime(t *testing.T) {
	_, server, db, fixture := newTestEnv(t)

	session := createSession(t, server, map[string]any{
		"mode": "exam", "scope": map[string]any{"term": termExam}, "durationMinutes": 150,
	}, http.StatusCreated)
	sessionID := mustString(t, session["id"])
	ids := questionIDList(t, session)
	if len(ids) != 2 || ids[0] != questionA || ids[1] != questionB {
		t.Fatalf("exam queue = %v, want [q-a q-b]", ids)
	}
	if session["deadlineAt"] == nil {
		t.Fatalf("exam session has no deadlineAt")
	}
	createdAt, err := time.Parse(time.RFC3339, mustString(t, session["createdAt"]))
	if err != nil {
		t.Fatalf("parse createdAt: %v", err)
	}
	deadlineAt, err := time.Parse(time.RFC3339, mustString(t, session["deadlineAt"]))
	if err != nil {
		t.Fatalf("parse deadlineAt: %v", err)
	}
	if diff := deadlineAt.Sub(createdAt); diff < 149*time.Minute || diff > 151*time.Minute {
		t.Fatalf("default exam duration = %v, want about 150m", diff)
	}

	// Detail before submission must not leak answers or correctness.
	status, body := server.Do(t, http.MethodGet, "/api/practice/sessions/"+sessionID, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	detail := sessionFrom(t, body)
	questions := mustList(t, body["questions"])
	if len(questions) != 2 {
		t.Fatalf("detail questions = %d, want 2", len(questions))
	}
	for _, raw := range questions {
		question := mustMap(t, raw)
		for _, forbidden := range []string{"answer", "analysis", "solution"} {
			if _, present := question[forbidden]; present {
				t.Fatalf("exam detail leaks %q: %v", forbidden, question)
			}
		}
	}
	if len(mustMap(t, detail["answers"])) != 0 {
		t.Fatalf("exam detail answers should be empty before submission")
	}

	answer := submitAnswer(t, server, sessionID, answerPayload("evt-exam-1", questionA, fixture.answer(questionA)), http.StatusOK)
	if revealed, _ := answer["revealed"].(bool); revealed {
		t.Fatalf("exam answer revealed before submit")
	}
	if answer["solution"] != nil {
		t.Fatalf("exam answer leaked solution before submit: %v", answer["solution"])
	}
	if _, present := answer["correct"]; present {
		t.Fatalf("exam answer leaked correctness before submit: %v", answer)
	}
	if _, present := mustMap(t, answer["attempt"])["correct"]; present {
		t.Fatalf("exam attempt leaked correctness before submit: %v", answer["attempt"])
	}
	// Disclosure must hold across projections, not only the session response.
	for _, path := range []string{"/api/practice/attempts", "/api/practice/wrong-book", "/api/learning/summary", "/api/learning/report", "/api/export/me"} {
		code, response := server.Do(t, http.MethodGet, path, nil)
		testutil.MustStatus(t, http.StatusOK, code, response)
		encoded, _ := json.Marshal(response)
		if strings.Contains(string(encoded), "evt-exam-1") {
			t.Fatalf("unsubmitted attempt leaked through %s: %s", path, encoded)
		}
	}
	code, stats := server.Do(t, http.MethodGet, "/api/practice/stats", nil)
	testutil.MustStatus(t, http.StatusOK, code, stats)
	if mustInt(t, mustMap(t, stats["totals"])["attempts"]) != 0 {
		t.Fatalf("exam leaked into statistics: %v", stats)
	}
	answerState := mustMap(t, mustMap(t, mustMap(t, answer["session"])["answers"])[questionA])
	if _, present := answerState["correct"]; present {
		t.Fatalf("exam session answer leaks correctness before submit: %v", answerState)
	}
	if answerState["revealed"] != false {
		t.Fatalf("exam session answer revealed = %v, want false", answerState["revealed"])
	}

	status, body = server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/submit", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if mustInt(t, body["score"]) != 1 || mustInt(t, body["maxScore"]) != 2 {
		t.Fatalf("exam score = %v/%v, want 1/2", body["score"], body["maxScore"])
	}
	results := mustList(t, body["results"])
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	firstResult := mustMap(t, results[0])
	if mustString(t, firstResult["questionId"]) != questionA || mustString(t, firstResult["answer"]) != fixture.answer(questionA) {
		t.Fatalf("first result = %v", firstResult)
	}
	if mustString(t, firstResult["analysis"]) == "" {
		t.Fatalf("result missing analysis")
	}
	if solution := mustMap(t, firstResult["solution"]); mustString(t, solution["answer"]) != fixture.answer(questionA) {
		t.Fatalf("result solution = %v", solution)
	}
	submitted := sessionFrom(t, body)
	if mustString(t, submitted["status"]) != "finished" || submitted["submittedAt"] == nil {
		t.Fatalf("submitted session = %v", submitted)
	}
	if mustInt(t, submitted["score"]) != 1 || mustInt(t, submitted["maxScore"]) != 2 {
		t.Fatalf("submitted session score = %v/%v", submitted["score"], submitted["maxScore"])
	}

	// Submitting twice returns the same recorded result.
	status, again := server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/submit", nil)
	testutil.MustStatus(t, http.StatusOK, status, again)
	if mustInt(t, again["score"]) != 1 {
		t.Fatalf("resubmit score = %v, want 1", again["score"])
	}

	// A second exam whose deadline has passed finalises with the answers
	// already received and reports deadline_passed.
	second := createSession(t, server, map[string]any{
		"mode": "exam", "scope": map[string]any{"term": termExam},
	}, http.StatusCreated)
	secondID := mustString(t, second["id"])
	submitAnswer(t, server, secondID, answerPayload("evt-dead-1", questionA, fixture.answer(questionA)), http.StatusOK)
	if _, err := db.Pool.Exec(context.Background(), `update attempts set answered_at = now() - interval '5 minutes' where id = 'evt-dead-1'`); err != nil {
		t.Fatalf("backdate attempt: %v", err)
	}
	if _, err := db.Pool.Exec(context.Background(), `update practice_sessions set deadline_at = now() - interval '1 minute' where id = $1::uuid`, secondID); err != nil {
		t.Fatalf("expire exam: %v", err)
	}

	status, body = server.Do(t, http.MethodPost, "/api/practice/sessions/"+secondID+"/answers",
		answerPayload("evt-dead-2", questionB, fixture.answer(questionB)))
	testutil.MustStatus(t, http.StatusConflict, status, body)
	if code := mustString(t, errorFrom(t, body)["code"]); code != "deadline_passed" {
		t.Fatalf("late answer code = %s, want deadline_passed", code)
	}

	status, body = server.Do(t, http.MethodPost, "/api/practice/sessions/"+secondID+"/submit", nil)
	testutil.MustStatus(t, http.StatusConflict, status, body)
	apiErr := errorFrom(t, body)
	if mustString(t, apiErr["code"]) != "deadline_passed" {
		t.Fatalf("late submit code = %v, want deadline_passed", apiErr["code"])
	}
	details := mustMap(t, apiErr["details"])
	if mustInt(t, details["score"]) != 1 || mustInt(t, details["maxScore"]) != 2 {
		t.Fatalf("late submit score = %v/%v, want 1/2", details["score"], details["maxScore"])
	}
	if mustString(t, mustMap(t, details["session"])["status"]) != "finished" {
		t.Fatalf("late submit did not finalise session: %v", details["session"])
	}

	// The recorded result is idempotent after the late submission.
	status, body = server.Do(t, http.MethodPost, "/api/practice/sessions/"+secondID+"/submit", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if mustInt(t, body["score"]) != 1 {
		t.Fatalf("post-deadline result score = %v, want 1", body["score"])
	}
}

func TestRevisionPinningKeepsHistoricalScore(t *testing.T) {
	_, server, db, fixture := newTestEnv(t)

	session := createSession(t, server, map[string]any{"mode": "all", "count": 10}, http.StatusCreated)
	sessionID := mustString(t, session["id"])
	submitAnswer(t, server, sessionID, answerPayload("evt-pin-1", questionA, fixture.answer(questionA)), http.StatusOK)

	// Publish a new revision for question A with a different answer and a
	// two-point rule. The active session must keep its pinned revision.
	optionsJSON := `{"A":"1","B":"2","C":"3","D":"4"}`
	var versionID string
	if err := db.Pool.QueryRow(context.Background(), `
		insert into question_versions (question_id, revision, stem, options, answer, analysis, scoring_rule)
		values ($1, 'rev2', '新版题干', $2::jsonb, 'A', '新版解析', '{"type":"single-choice","points":2,"ruleVersion":"v1"}'::jsonb)
		returning id::text`, questionA, optionsJSON).Scan(&versionID); err != nil {
		t.Fatalf("insert rev2: %v", err)
	}
	if _, err := db.Pool.Exec(context.Background(), `update questions set current_version_id = $2::uuid where id = $1`, questionA, versionID); err != nil {
		t.Fatalf("publish rev2: %v", err)
	}

	status, body := server.Do(t, http.MethodGet, "/api/practice/sessions/"+sessionID, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if revision := mustString(t, mustMap(t, sessionFrom(t, body)["revisions"])[questionA]); revision != "rev1" {
		t.Fatalf("active session revision = %s, want pinned rev1", revision)
	}
	for _, raw := range mustList(t, body["questions"]) {
		question := mustMap(t, raw)
		if mustString(t, question["id"]) == questionA && mustString(t, question["revision"]) != "rev1" {
			t.Fatalf("detail shows unpinned revision: %v", question)
		}
	}

	status, body = server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/finish", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	finished := sessionFrom(t, body)
	if mustInt(t, finished["score"]) != 1 || mustInt(t, finished["maxScore"]) != 4 {
		t.Fatalf("historical score = %v/%v, want 1/4 (rev1 rules)", finished["score"], finished["maxScore"])
	}
	if got := countRows(t, db, `select count(*) from attempts where id = 'evt-pin-1' and question_revision = 'rev1'`); got != 1 {
		t.Fatalf("attempt revision was not pinned to rev1")
	}

	// A new session picks up the new revision and its two-point rule.
	newSession := createSession(t, server, map[string]any{"mode": "all", "count": 10}, http.StatusCreated)
	newSessionID := mustString(t, newSession["id"])
	if revision := mustString(t, mustMap(t, newSession["revisions"])[questionA]); revision != "rev2" {
		t.Fatalf("new session revision = %s, want rev2", revision)
	}
	answer := submitAnswer(t, server, newSessionID, answerPayload("evt-pin-2", questionA, "A"), http.StatusOK)
	if correct, _ := answer["correct"].(bool); !correct {
		t.Fatalf("new revision answer should be correct")
	}
	solution := mustMap(t, answer["solution"])
	if mustString(t, solution["analysis"]) != "新版解析" {
		t.Fatalf("new revision solution = %v", solution)
	}
	status, body = server.Do(t, http.MethodPost, "/api/practice/sessions/"+newSessionID+"/finish", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if score := mustInt(t, sessionFrom(t, body)["score"]); score != 2 {
		t.Fatalf("new revision score = %d, want 2", score)
	}
}

func TestAnswerValidation(t *testing.T) {
	_, server, _, fixture := newTestEnv(t)
	session := createSession(t, server, map[string]any{"mode": "all", "count": 2}, http.StatusCreated)
	sessionID := mustString(t, session["id"])

	cases := []struct {
		name string
		body map[string]any
		code string
	}{
		{name: "missing event id", body: map[string]any{"questionId": questionA, "selected": "A"}, code: "event_id_required"},
		{name: "missing question", body: map[string]any{"id": "evt-x", "selected": "A"}, code: "question_required"},
		{name: "missing answer", body: map[string]any{"id": "evt-x", "questionId": questionA}, code: "answer_required"},
		{name: "question not in session", body: map[string]any{"id": "evt-x", "questionId": questionWithdrawn, "selected": "A"}, code: "question_not_in_session"},
		{name: "bad answeredAt", body: map[string]any{"id": "evt-x", "questionId": questionA, "selected": "A", "answeredAt": "yesterday"}, code: "invalid_answered_at"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, body := server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/answers", testCase.body)
			testutil.MustStatus(t, http.StatusBadRequest, status, body)
			if code := mustString(t, errorFrom(t, body)["code"]); code != testCase.code {
				t.Fatalf("code = %s, want %s", code, testCase.code)
			}
		})
	}

	// The event id is globally idempotent: reusing it in another session is an
	// explicit conflict rather than silent double counting.
	submitAnswer(t, server, sessionID, answerPayload("evt-shared", questionA, fixture.answer(questionA)), http.StatusOK)
	status, body := server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/finish", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	other := createSession(t, server, map[string]any{"mode": "all", "count": 2}, http.StatusCreated)
	status, body = server.Do(t, http.MethodPost, "/api/practice/sessions/"+mustString(t, other["id"])+"/answers",
		answerPayload("evt-shared", questionA, fixture.answer(questionA)))
	testutil.MustStatus(t, http.StatusBadRequest, status, body)
	if code := mustString(t, errorFrom(t, body)["code"]); code != "event_id_conflict" {
		t.Fatalf("reused event id code = %s, want event_id_conflict", code)
	}
}

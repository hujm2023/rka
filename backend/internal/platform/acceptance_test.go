// Package platform_test exercises the assembled application across domain
// boundaries through the public HTTP surface. These tests are the executable
// form of the spec's behaviour acceptance matrix.
package platform_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"rka/internal/app"
	"rka/internal/testutil"
)

const password = "platform-test-password"

func setup(t *testing.T) (*app.App, *testutil.Server) {
	t.Helper()
	application, server, _ := testutil.NewApp(t)
	testutil.ImportContent(t, application)
	return application, server
}

func obj(value any) map[string]any {
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return nil
}

func arr(value any) []any {
	if a, ok := value.([]any); ok {
		return a
	}
	return nil
}

func str(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func num(value any) float64 {
	if n, ok := value.(float64); ok {
		return n
	}
	return 0
}

// linkedQuestion returns a question with a knowledge link and its material.
func linkedQuestion(t *testing.T, catalog map[string]any) (questionID, materialID, sectionID string) {
	t.Helper()
	links := obj(catalog["knowledgeLinks"])
	questions := map[string]bool{}
	for _, raw := range arr(catalog["questions"]) {
		questions[str(obj(raw)["id"])] = true
	}
	for qid, raw := range links {
		if !questions[qid] {
			continue
		}
		for _, item := range arr(raw) {
			link := obj(item)
			if str(link["materialId"]) == "" || str(link["sectionId"]) == "" {
				continue
			}
			if str(link["status"]) == "rejected" {
				continue
			}
			return qid, str(link["materialId"]), str(link["sectionId"])
		}
	}
	t.Fatal("catalog has no linked question")
	return "", "", ""
}

func rawGet(t *testing.T, server *testutil.Server, path string) (int, string) {
	t.Helper()
	response, err := server.Client.Get(server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	return response.StatusCode, string(body)
}

func TestLearningLoopAcrossDomains(t *testing.T) {
	application, server := setup(t)
	learner := testutil.CreateUser(t, application, "learner@example.com", password, "learner")
	testutil.Login(t, server, learner.Email, password)

	status, catalog := server.Do(t, http.MethodGet, "/api/content/catalog", nil)
	testutil.MustStatus(t, http.StatusOK, status, catalog)
	questions := arr(catalog["questions"])
	if len(questions) < 1000 {
		t.Fatalf("catalog questions = %d, want the bundled snapshot", len(questions))
	}
	first := obj(questions[0])
	if _, leaked := first["answer"]; leaked {
		t.Fatal("catalog leaks the reference answer")
	}
	if _, leaked := first["analysis"]; leaked {
		t.Fatal("catalog leaks the analysis")
	}

	linkedQID, materialID, sectionID := linkedQuestion(t, catalog)

	status, material := server.Do(t, http.MethodGet, "/api/content/materials/"+materialID, nil)
	testutil.MustStatus(t, http.StatusOK, status, material)
	materialDoc := obj(material["material"])
	versionID := str(materialDoc["versionId"])
	if versionID == "" {
		t.Fatalf("material has no versionId: %v", materialDoc)
	}
	status, content := server.Do(t, http.MethodGet, "/api/content/materials/"+materialID+"/content", nil)
	testutil.MustStatus(t, http.StatusOK, status, content)
	if str(content["content"]) == "" {
		t.Fatal("material content is empty")
	}
	status, rawBody := rawGet(t, server, "/api/content/materials/"+materialID+"/raw")
	if status != http.StatusOK || rawBody == "" {
		t.Fatalf("raw download status = %d, bytes = %d", status, len(rawBody))
	}

	// Reading evidence.
	status, body := server.Do(t, http.MethodPost, "/api/learning/reading", map[string]any{
		"materialId": materialID, "materialVersionId": versionID,
		"progress": 0.25, "sectionId": sectionID, "offset": 0, "done": false,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)

	// Practice inside the material scope.
	status, sessionBody := server.Do(t, http.MethodPost, "/api/practice/sessions", map[string]any{
		"mode": "chapter", "scope": map[string]any{"materialId": materialID}, "count": 5,
	})
	testutil.MustStatus(t, http.StatusCreated, status, sessionBody)
	session := obj(sessionBody["session"])
	sessionID := str(session["id"])
	questionIDs := arr(session["questionIds"])
	if sessionID == "" || len(questionIDs) == 0 {
		t.Fatalf("session was not created: %v", sessionBody)
	}
	_ = linkedQID

	questionID := str(questionIDs[0])
	eventID := "platform-event-1"
	answerPayload := map[string]any{
		"id": eventID, "questionId": questionID, "selected": "A", "assisted": false, "durationMs": 1500,
	}
	status, answerBody := server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/answers", answerPayload)
	testutil.MustStatus(t, http.StatusOK, status, answerBody)
	if answerBody["revealed"] != true {
		t.Fatalf("practice answer must reveal the solution: %v", answerBody)
	}
	solution := obj(answerBody["solution"])
	if str(solution["answer"]) == "" {
		t.Fatalf("practice answer returned no solution: %v", answerBody)
	}

	// Replaying the same event is idempotent.
	status, replay := server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/answers", answerPayload)
	testutil.MustStatus(t, http.StatusOK, status, replay)
	status, attempts := server.Do(t, http.MethodGet, "/api/practice/attempts?questionId="+questionID, nil)
	testutil.MustStatus(t, http.StatusOK, status, attempts)
	if rows := arr(attempts["attempts"]); len(rows) != 1 {
		if rows := arr(attempts["items"]); len(rows) != 1 {
			t.Fatalf("replayed answer created %d attempt rows, want 1: %v", len(rows), attempts)
		}
	}

	// The same session is visible from a second device.
	_, secondDevice, _ := testutil.NewAppFor(t, application)
	testutil.Login(t, secondDevice, learner.Email, password)
	status, active := secondDevice.Do(t, http.MethodGet, "/api/practice/sessions/active", nil)
	testutil.MustStatus(t, http.StatusOK, status, active)
	activeSession := obj(active["session"])
	if activeSession == nil || str(activeSession["id"]) != sessionID {
		t.Fatalf("second device did not restore the active session: %v", active)
	}
	answerState := obj(obj(activeSession["answers"])[questionID])
	if answerState == nil || answerState["submitted"] != true {
		t.Fatalf("second device did not restore the submitted answer: %v", activeSession["answers"])
	}

	// Learning evidence is derived from the facts and can be traced back.
	status, summary := server.Do(t, http.MethodGet, "/api/learning/summary", nil)
	testutil.MustStatus(t, http.StatusOK, status, summary)
	counts := obj(summary["counts"])
	wrong := num(counts["wrong"])
	assisted := num(counts["assisted_pass"])
	independent := num(counts["independent_pass"])
	if wrong+assisted+independent != 1 {
		t.Fatalf("expected exactly one attempt-derived evidence, got %v", counts)
	}
	if num(counts["reading"]) < 1 {
		t.Fatalf("reading evidence was not recorded: %v", counts)
	}

	status, report := server.Do(t, http.MethodGet, "/api/learning/report", nil)
	testutil.MustStatus(t, http.StatusOK, status, report)
	basis := arr(report["basis"])
	if len(basis) == 0 {
		t.Fatalf("report has no evidence basis: %v", report)
	}
	ids := arr(obj(basis[0])["evidenceIds"])
	if len(ids) == 0 {
		t.Fatalf("report basis cannot be traced to evidence: %v", basis[0])
	}

	// Writing: versioned draft and a conflict that keeps both versions.
	status, emptyDoc := server.Do(t, http.MethodGet, "/api/writing/essay/essay-mock-2026年5月-模拟卷1-1", nil)
	testutil.MustStatus(t, http.StatusOK, status, emptyDoc)
	status, saved := server.Do(t, http.MethodPut, "/api/writing/essay/essay-mock-2026年5月-模拟卷1-1", map[string]any{
		"values": map[string]any{"material": "项目素材", "body": "第一版正文"}, "version": 0,
	})
	testutil.MustStatus(t, http.StatusOK, status, saved)
	if version := num(obj(saved["document"])["version"]); version != 1 {
		t.Fatalf("writing version = %v, want 1", version)
	}
	status, conflict := server.Do(t, http.MethodPut, "/api/writing/essay/essay-mock-2026年5月-模拟卷1-1", map[string]any{
		"values": map[string]any{"material": "并发版本"}, "version": 0,
	})
	testutil.MustStatus(t, http.StatusConflict, status, conflict)
	details := obj(obj(conflict["error"])["details"])
	if details == nil || num(details["serverVersion"]) != 1 {
		t.Fatalf("conflict must return the server version: %v", conflict)
	}

	// AI is explicitly unavailable without configuration.
	status, task := server.Do(t, http.MethodPost, "/api/ai/tasks", map[string]any{
		"kind": "hint", "idempotencyKey": "platform-ai-1",
		"context": map[string]any{"questionId": questionID},
	})
	testutil.MustStatus(t, http.StatusServiceUnavailable, status, task)
	if code := str(obj(task["error"])["code"]); code != "ai_unavailable" {
		t.Fatalf("ai error code = %q, want ai_unavailable", code)
	}
	status, aiStatus := server.Do(t, http.MethodGet, "/api/ai/status", nil)
	testutil.MustStatus(t, http.StatusOK, status, aiStatus)
	if str(aiStatus["unavailableReason"]) == "" {
		t.Fatalf("ai status must explain why AI is unavailable: %v", aiStatus)
	}

	// Data autonomy: export contains the facts, deletion keeps the account.
	status, exported := server.Do(t, http.MethodGet, "/api/export/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, exported)
	exportText := fmt.Sprint(exported)
	for _, needle := range []string{"attempts", "reading", "writing"} {
		if !strings.Contains(exportText, needle) {
			t.Fatalf("export does not include %s: %.300s", needle, exportText)
		}
	}
	status, deleted := server.Do(t, http.MethodDelete, "/api/me/data", map[string]any{"confirm": "DELETE"})
	testutil.MustStatus(t, http.StatusOK, status, deleted)
	status, me := server.Do(t, http.MethodGet, "/api/auth/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, me)
	if me["user"] == nil {
		t.Fatal("deleting learning data must keep the account")
	}
}

func TestIdentityIsolationAcrossUsers(t *testing.T) {
	application, first := setup(t)
	alice := testutil.CreateUser(t, application, "alice@example.com", password, "learner")
	bob := testutil.CreateUser(t, application, "bob@example.com", password, "learner")
	testutil.Login(t, first, alice.Email, password)

	status, sessionBody := first.Do(t, http.MethodPost, "/api/practice/sessions", map[string]any{"mode": "all", "count": 3})
	testutil.MustStatus(t, http.StatusCreated, status, sessionBody)
	sessionID := str(obj(sessionBody["session"])["id"])
	questionID := str(arr(obj(sessionBody["session"])["questionIds"])[0])
	status, _ = first.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/answers", map[string]any{
		"id": "alice-event-1", "questionId": questionID, "selected": "B",
	})
	testutil.MustStatus(t, http.StatusOK, status, nil)
	status, _ = first.Do(t, http.MethodPut, "/api/writing/case/case-mock-2026年5月-模拟卷1-1", map[string]any{
		"values": map[string]any{"q1": "alice 的私有作答"}, "version": 0,
	})
	testutil.MustStatus(t, http.StatusOK, status, nil)

	_, second, _ := testutil.NewAppFor(t, application)
	testutil.Login(t, second, bob.Email, password)

	status, body := second.Do(t, http.MethodGet, "/api/practice/sessions/"+sessionID, nil)
	if status != http.StatusNotFound {
		t.Fatalf("bob can read alice's session: status=%d body=%v", status, body)
	}
	status, attempts := second.Do(t, http.MethodGet, "/api/practice/attempts?questionId="+questionID, nil)
	testutil.MustStatus(t, http.StatusOK, status, attempts)
	rows := arr(attempts["attempts"])
	if len(rows) == 0 {
		rows = arr(attempts["items"])
	}
	if len(rows) != 0 {
		t.Fatalf("bob sees alice's attempts: %v", attempts)
	}
	status, doc := second.Do(t, http.MethodGet, "/api/writing/case/case-mock-2026年5月-模拟卷1-1", nil)
	testutil.MustStatus(t, http.StatusOK, status, doc)
	values := obj(obj(doc["document"])["values"])
	if len(values) != 0 {
		t.Fatalf("bob sees alice's draft: %v", doc)
	}
}

func TestGrantAndRevocationTakeEffectImmediately(t *testing.T) {
	application, adminServer := setup(t)
	admin := testutil.CreateUser(t, application, "admin@example.com", password, "admin")
	learner := testutil.CreateUser(t, application, "learner@example.com", password, "learner")
	testutil.Login(t, adminServer, admin.Email, password)

	status, catalog := adminServer.Do(t, http.MethodGet, "/api/content/catalog", nil)
	testutil.MustStatus(t, http.StatusOK, status, catalog)
	materialID := str(obj(arr(catalog["materials"])[0])["id"])

	status, _ = adminServer.Do(t, http.MethodPatch, "/api/admin/content/materials/"+materialID+"/review",
		map[string]any{"reviewStatus": "withdrawn"})
	testutil.MustStatus(t, http.StatusOK, status, nil)

	_, learnerServer, _ := testutil.NewAppFor(t, application)
	testutil.Login(t, learnerServer, learner.Email, password)
	status, body := learnerServer.Do(t, http.MethodGet, "/api/content/materials/"+materialID, nil)
	if status != http.StatusForbidden {
		t.Fatalf("withdrawn material should be forbidden for learners, got %d %v", status, body)
	}

	status, grant := adminServer.Do(t, http.MethodPost, "/api/admin/grants", map[string]any{
		"userId": learner.ID, "resourceType": "material", "resourceId": materialID,
	})
	testutil.MustStatus(t, http.StatusCreated, status, grant)
	status, body = learnerServer.Do(t, http.MethodGet, "/api/content/materials/"+materialID, nil)
	testutil.MustStatus(t, http.StatusOK, status, body)

	status, _ = adminServer.Do(t, http.MethodDelete, "/api/admin/grants/"+str(grant["id"]), nil)
	testutil.MustStatus(t, http.StatusOK, status, nil)
	status, body = learnerServer.Do(t, http.MethodGet, "/api/content/materials/"+materialID, nil)
	if status != http.StatusForbidden {
		t.Fatalf("revoked grant must take effect immediately, got %d %v", status, body)
	}
}

func TestExamHidesAnswersAndHonoursServerDeadline(t *testing.T) {
	application, server := setup(t)
	learner := testutil.CreateUser(t, application, "exam@example.com", password, "learner")
	testutil.Login(t, server, learner.Email, password)

	status, catalog := server.Do(t, http.MethodGet, "/api/content/catalog", nil)
	testutil.MustStatus(t, http.StatusOK, status, catalog)
	var term string
	for _, raw := range arr(catalog["questions"]) {
		if t0 := str(obj(raw)["term"]); t0 != "" && obj(raw)["sourceType"] == "real" {
			term = t0
			break
		}
	}
	if term == "" {
		t.Skip("no exam term in catalog")
	}
	status, sessionBody := server.Do(t, http.MethodPost, "/api/practice/sessions", map[string]any{
		"mode": "exam", "scope": map[string]any{"term": term}, "count": 5, "durationMinutes": 60,
	})
	testutil.MustStatus(t, http.StatusCreated, status, sessionBody)
	session := obj(sessionBody["session"])
	sessionID := str(session["id"])
	if str(session["deadlineAt"]) == "" {
		t.Fatalf("exam session has no server deadline: %v", session)
	}
	questionID := str(arr(session["questionIds"])[0])
	status, answer := server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/answers", map[string]any{
		"id": "exam-event-1", "questionId": questionID, "selected": "A",
	})
	testutil.MustStatus(t, http.StatusOK, status, answer)
	if answer["revealed"] != false {
		t.Fatalf("exam revealed the answer before submission: %v", answer)
	}
	if answer["solution"] != nil {
		t.Fatalf("exam returned a solution before submission: %v", answer)
	}

	// The deadline is enforced by server state, not by the client.
	if _, err := application.DB.Pool.Exec(t.Context(),
		`update practice_sessions set deadline_at = now() - interval '1 minute' where id = $1::uuid`, sessionID); err != nil {
		t.Fatalf("move deadline: %v", err)
	}
	status, body := server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/submit", map[string]any{})
	testutil.MustStatus(t, http.StatusConflict, status, body)
	if code := str(obj(body["error"])["code"]); code != "deadline_passed" {
		t.Fatalf("error code = %q, want deadline_passed", code)
	}
}

func TestModelConfigStatusIsHonest(t *testing.T) {
	application, server := setup(t)
	admin := testutil.CreateUser(t, application, "admin@example.com", password, "admin")
	testutil.Login(t, server, admin.Email, password)

	status, list := server.Do(t, http.MethodGet, "/api/admin/model-configs", nil)
	testutil.MustStatus(t, http.StatusOK, status, list)
	env := obj(list["environment"])
	if env["configured"] != false {
		t.Fatalf("test environment should have no configured model: %v", env)
	}

	status, created := server.Do(t, http.MethodPost, "/api/admin/model-configs", map[string]any{
		"name": "测试模型", "kind": "llm", "endpoint": "http://127.0.0.1:1/v1/chat/completions",
		"model": "test-model", "apiKey": "super-secret-key",
	})
	testutil.MustStatus(t, http.StatusCreated, status, created)
	config := obj(created["config"])
	if config["configured"] != true {
		t.Fatalf("config with endpoint+model must be configured: %v", config)
	}
	if config["connectionStatus"] != "unknown" || config["capabilityStatus"] != "unverified" {
		t.Fatalf("configured must not be reported as connected or verified: %v", config)
	}
	if text := fmt.Sprint(created); strings.Contains(text, "super-secret-key") {
		t.Fatal("model credential leaked through the API")
	}

	id := str(config["id"])
	status, checked := server.Do(t, http.MethodPost, "/api/admin/model-configs/"+id+"/check", map[string]any{})
	testutil.MustStatus(t, http.StatusOK, status, checked)
	status, list = server.Do(t, http.MethodGet, "/api/admin/model-configs", nil)
	testutil.MustStatus(t, http.StatusOK, status, list)
	for _, raw := range arr(list["items"]) {
		item := obj(raw)
		if str(item["id"]) != id {
			continue
		}
		if item["connectionStatus"] != "failed" {
			t.Fatalf("unreachable model must report failed connection: %v", item)
		}
	}
}

func TestSecondDeviceRestoresServerConfirmedSession(t *testing.T) {
	application, first := setup(t)
	learner := testutil.CreateUser(t, application, "device@example.com", password, "learner")
	testutil.Login(t, first, learner.Email, password)
	status, sessionBody := first.Do(t, http.MethodPost, "/api/practice/sessions", map[string]any{"mode": "all", "count": 4})
	testutil.MustStatus(t, http.StatusCreated, status, sessionBody)
	sessionID := str(obj(sessionBody["session"])["id"])
	questionIDs := arr(obj(sessionBody["session"])["questionIds"])
	status, _ = first.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/answers", map[string]any{
		"id": "device-event-1", "questionId": str(questionIDs[0]), "selected": "C",
	})
	testutil.MustStatus(t, http.StatusOK, status, nil)
	status, _ = first.Do(t, http.MethodPatch, "/api/practice/sessions/"+sessionID, map[string]any{
		"index": 2, "elapsedSeconds": 90, "version": 1,
	})
	testutil.MustStatus(t, http.StatusOK, status, nil)

	_, second, _ := testutil.NewAppFor(t, application)
	testutil.Login(t, second, learner.Email, password)
	status, active := second.Do(t, http.MethodGet, "/api/practice/sessions/active", nil)
	testutil.MustStatus(t, http.StatusOK, status, active)
	session := obj(active["session"])
	if session == nil || str(session["id"]) != sessionID {
		t.Fatalf("second device did not restore the session: %v", active)
	}
	if num(session["index"]) != 2 || num(session["elapsedSeconds"]) != 90 {
		t.Fatalf("second device did not restore position/time: %v", session)
	}
}

// TestExportDeleteAndReimportRestoresLearningData covers the data-autonomy
// promise end to end: the personal export is complete, deleting learning data
// keeps the account, and re-importing the export restores the same facts
// without duplicating them on a second import.
func TestExportDeleteAndReimportRestoresLearningData(t *testing.T) {
	application, server := setup(t)
	learner := testutil.CreateUser(t, application, "backup@example.com", password, "learner")
	testutil.Login(t, server, learner.Email, password)

	status, catalog := server.Do(t, http.MethodGet, "/api/content/catalog", nil)
	testutil.MustStatus(t, http.StatusOK, status, catalog)
	material := obj(arr(catalog["materials"])[0])
	materialID := str(material["id"])
	versionID := str(material["versionId"])
	sections := arr(material["sections"])
	if materialID == "" || versionID == "" || len(sections) == 0 {
		t.Fatalf("catalog material is incomplete: %v", material)
	}
	sectionID := str(obj(sections[0])["id"])
	cases := arr(catalog["cases"])
	if len(cases) == 0 {
		t.Fatal("catalog has no case target")
	}
	caseID := str(obj(cases[0])["id"])

	status, _ = server.Do(t, http.MethodPost, "/api/learning/reading", map[string]any{
		"materialId": materialID, "materialVersionId": versionID,
		"progress": 0.6, "sectionId": sectionID, "offset": 0, "done": true,
	})
	testutil.MustStatus(t, http.StatusOK, status, nil)

	status, sessionBody := server.Do(t, http.MethodPost, "/api/practice/sessions", map[string]any{"mode": "all", "count": 2})
	testutil.MustStatus(t, http.StatusCreated, status, sessionBody)
	session := obj(sessionBody["session"])
	sessionID := str(session["id"])
	questionID := str(arr(session["questionIds"])[0])
	status, _ = server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/answers", map[string]any{
		"id": "backup-event-1", "questionId": questionID, "selected": "A", "assisted": false, "durationMs": 900,
	})
	testutil.MustStatus(t, http.StatusOK, status, nil)
	status, _ = server.Do(t, http.MethodPut, "/api/practice/marks/"+questionID, map[string]any{
		"favorite": true, "uncertain": false, "note": "备份前收藏", "version": 0,
	})
	testutil.MustStatus(t, http.StatusOK, status, nil)
	status, _ = server.Do(t, http.MethodPut, "/api/writing/case/"+caseID, map[string]any{
		"values": map[string]any{"answer-0": "备份前的案例作答"}, "version": 0,
	})
	testutil.MustStatus(t, http.StatusOK, status, nil)

	status, exported := server.Do(t, http.MethodGet, "/api/export/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, exported)
	if num(exported["schemaVersion"]) != 2 {
		t.Fatalf("export schemaVersion = %v, want 2", exported["schemaVersion"])
	}
	before := obj(exported["counts"])
	for _, key := range []string{"attempts", "sessions", "marks", "reading", "writingDocuments"} {
		if num(before[key]) < 1 {
			t.Fatalf("export counts.%s = %v, want at least 1: %v", key, before[key], before)
		}
	}

	// Deleting learning data keeps the account but clears the facts.
	status, _ = server.Do(t, http.MethodDelete, "/api/me/data", map[string]any{"confirm": "DELETE"})
	testutil.MustStatus(t, http.StatusOK, status, nil)
	status, cleared := server.Do(t, http.MethodGet, "/api/export/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, cleared)
	afterDelete := obj(cleared["counts"])
	if num(afterDelete["attempts"]) != 0 || num(afterDelete["writingDocuments"]) != 0 || num(afterDelete["marks"]) != 0 {
		t.Fatalf("delete kept learning data: %v", afterDelete)
	}
	status, me := server.Do(t, http.MethodGet, "/api/auth/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, me)
	if me["user"] == nil {
		t.Fatal("deleting learning data must keep the account")
	}

	// Re-importing the export restores the facts.
	status, imported := server.Do(t, http.MethodPost, "/api/sync/import", exported)
	testutil.MustStatus(t, http.StatusOK, status, imported)
	importedCounts := obj(imported["imported"])
	if num(importedCounts["attempts"]) != 1 || num(importedCounts["writingDocuments"]) != 1 || num(importedCounts["reading"]) < 1 {
		t.Fatalf("re-import did not restore the facts: %v", importedCounts)
	}

	status, restored := server.Do(t, http.MethodGet, "/api/export/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, restored)
	restoredCounts := obj(restored["counts"])
	for _, key := range []string{"attempts", "sessions", "marks", "reading", "writingDocuments"} {
		if num(restoredCounts[key]) != num(before[key]) {
			t.Fatalf("restored counts.%s = %v, want %v: %v", key, restoredCounts[key], before[key], restoredCounts)
		}
	}
	status, attempts := server.Do(t, http.MethodGet, "/api/practice/attempts?questionId="+questionID, nil)
	testutil.MustStatus(t, http.StatusOK, status, attempts)
	rows := arr(attempts["attempts"])
	if len(rows) == 0 {
		rows = arr(attempts["items"])
	}
	if len(rows) != 1 {
		t.Fatalf("restored attempt rows = %d, want 1: %v", len(rows), attempts)
	}
	status, document := server.Do(t, http.MethodGet, "/api/writing/case/"+caseID, nil)
	testutil.MustStatus(t, http.StatusOK, status, document)
	if saved := obj(obj(document["document"])["values"])["answer-0"]; saved != "备份前的案例作答" {
		t.Fatalf("restored case answer = %v, want the exported value", saved)
	}

	// A second import is idempotent: nothing is duplicated.
	status, replay := server.Do(t, http.MethodPost, "/api/sync/import", exported)
	testutil.MustStatus(t, http.StatusOK, status, replay)
	if num(obj(replay["imported"])["attempts"]) != 0 {
		t.Fatalf("replayed import duplicated attempts: %v", replay["imported"])
	}
	status, final := server.Do(t, http.MethodGet, "/api/export/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, final)
	if num(obj(final["counts"])["attempts"]) != num(before["attempts"]) {
		t.Fatalf("replayed import changed the attempt count: %v -> %v", before["attempts"], obj(final["counts"])["attempts"])
	}
}

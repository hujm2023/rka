package writing_test

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"

	"rka/internal/testutil"
	"rka/internal/writing"
)

func TestDocumentLifecycleAndVersionHistory(t *testing.T) {
	env := newTestEnv(t)
	insertSubjectiveItem(t, env.db, "case-1", "case")
	env.createUser("writer@example.com")
	env.login(env.server, "writer@example.com")

	// An unsaved draft is an empty version-0 document so the client can create
	// it with a normal optimistic-concurrency check.
	status, body := env.server.Do(t, http.MethodGet, "/api/writing/case/case-1", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	doc := documentOf(t, body)
	if got := numberValue(t, doc, "version"); got != 0 {
		t.Fatalf("empty document version = %d, want 0", got)
	}
	if got := stringValue(doc, "id"); got != "" {
		t.Fatalf("empty document id = %q, want empty", got)
	}
	if values := objectValue(t, doc["values"], "document.values"); len(values) != 0 {
		t.Fatalf("empty document values = %v, want {}", values)
	}

	status, body = env.putDocument(env.server, "case", "case-1", map[string]string{
		"answer-0": "第一版答案",
	}, 0)
	testutil.MustStatus(t, http.StatusOK, status, body)
	doc = documentOf(t, body)
	documentID := stringValue(doc, "id")
	if documentID == "" {
		t.Fatal("saved document has no id")
	}
	if got := numberValue(t, doc, "version"); got != 1 {
		t.Fatalf("first save version = %d, want 1", got)
	}

	status, body = env.putDocument(env.server, "case", "case-1", map[string]string{
		"answer-0": "第二版答案",
		"answer-1": "补充第二问",
	}, 1)
	testutil.MustStatus(t, http.StatusOK, status, body)
	doc = documentOf(t, body)
	if got := numberValue(t, doc, "version"); got != 2 {
		t.Fatalf("second save version = %d, want 2", got)
	}
	if got := stringValue(doc, "id"); got != documentID {
		t.Fatalf("document id changed: %q -> %q", documentID, got)
	}

	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-1", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	doc = documentOf(t, body)
	if got := numberValue(t, doc, "version"); got != 2 {
		t.Fatalf("GET version = %d, want 2", got)
	}
	values := objectValue(t, doc["values"], "document.values")
	if values["answer-0"] != "第二版答案" || values["answer-1"] != "补充第二问" {
		t.Fatalf("GET values = %v", values)
	}

	// Version history is newest first and records every successful PUT.
	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-1/versions", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items := arrayValue(t, body["items"], "items")
	if len(items) != 2 {
		t.Fatalf("version count = %d, want 2: %v", len(items), items)
	}
	first := objectValue(t, items[0], "items[0]")
	if numberValue(t, first, "version") != 2 || stringValue(first, "source") != "user" {
		t.Fatalf("items[0] = %v, want version 2 user", first)
	}
	second := objectValue(t, items[1], "items[1]")
	if numberValue(t, second, "version") != 1 || stringValue(second, "source") != "user" {
		t.Fatalf("items[1] = %v, want version 1 user", second)
	}

	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-1/versions/1", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	version := objectValue(t, body["version"], "version")
	if numberValue(t, version, "version") != 1 || stringValue(version, "source") != "user" {
		t.Fatalf("version detail = %v", version)
	}
	firstValues := objectValue(t, version["values"], "version.values")
	if firstValues["answer-0"] != "第一版答案" {
		t.Fatalf("version 1 values = %v", firstValues)
	}

	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-1/versions/99", nil)
	testutil.MustStatus(t, http.StatusNotFound, status, body)

	// Essay sections are stored independently under the documented keys.
	insertSubjectiveItem(t, env.db, "essay-1", "essay")
	status, body = env.putDocument(env.server, "essay", "essay-1", map[string]string{
		"material": "项目素材",
		"outline":  "提纲",
		"abstract": "摘要",
		"body":     "正文",
	}, 0)
	testutil.MustStatus(t, http.StatusOK, status, body)
	doc = documentOf(t, body)
	if got := numberValue(t, doc, "version"); got != 1 {
		t.Fatalf("essay version = %d, want 1", got)
	}
	essayValues := objectValue(t, doc["values"], "essay.values")
	for _, key := range []string{"material", "outline", "abstract", "body"} {
		if essayValues[key] == "" {
			t.Fatalf("essay values missing %q: %v", key, essayValues)
		}
	}
}

func TestDocumentBoundaries(t *testing.T) {
	env := newTestEnv(t)
	insertSubjectiveItem(t, env.db, "case-1", "case")
	env.createUser("writer@example.com")
	env.login(env.server, "writer@example.com")

	// Invalid kind is rejected before any target lookup.
	status, body := env.server.Do(t, http.MethodGet, "/api/writing/paper/case-1", nil)
	testutil.MustStatus(t, http.StatusBadRequest, status, body)
	if code := errorCode(t, body); code != "invalid_kind" {
		t.Fatalf("error code = %q, want invalid_kind", code)
	}

	// Unknown target ids are 404 for reads and writes.
	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/missing-case", nil)
	testutil.MustStatus(t, http.StatusNotFound, status, body)
	status, body = env.putDocument(env.server, "case", "missing-case", map[string]string{"answer-0": "x"}, 0)
	testutil.MustStatus(t, http.StatusNotFound, status, body)

	// A target of the other kind does not match.
	insertSubjectiveItem(t, env.db, "essay-1", "essay")
	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/essay-1", nil)
	testutil.MustStatus(t, http.StatusNotFound, status, body)

	// PUT requires an explicit version and a values object.
	status, body = env.server.Do(t, http.MethodPut, "/api/writing/case/case-1", map[string]any{
		"values": map[string]string{"answer-0": "x"},
	})
	testutil.MustStatus(t, http.StatusBadRequest, status, body)
	if code := errorCode(t, body); code != "version_required" {
		t.Fatalf("error code = %q, want version_required", code)
	}
	status, body = env.server.Do(t, http.MethodPut, "/api/writing/case/case-1", map[string]any{
		"version": 0,
	})
	testutil.MustStatus(t, http.StatusBadRequest, status, body)
	if code := errorCode(t, body); code != "values_required" {
		t.Fatalf("error code = %q, want values_required", code)
	}

	// Drafts above 200KB are rejected with 413.
	oversized := strings.Repeat("答", writing.MaxValuesBytes/3+1024)
	status, body = env.putDocument(env.server, "case", "case-1", map[string]string{"answer-0": oversized}, 0)
	testutil.MustStatus(t, http.StatusRequestEntityTooLarge, status, body)
	if code := errorCode(t, body); code != "too_large" {
		t.Fatalf("error code = %q, want too_large", code)
	}
	// The rejected oversized write never created a document.
	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-1", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if got := numberValue(t, documentOf(t, body), "version"); got != 0 {
		t.Fatalf("oversized write created version %d", got)
	}

	// A versioned write against a missing document conflicts with version 0.
	status, body = env.putDocument(env.server, "case", "case-1", map[string]string{"answer-0": "x"}, 3)
	testutil.MustStatus(t, http.StatusConflict, status, body)
	details := detailsOf(t, body)
	if got := numberValue(t, details, "serverVersion"); got != 0 {
		t.Fatalf("serverVersion = %d, want 0", got)
	}

	// Reviews need a saved draft.
	status, body = env.server.Do(t, http.MethodPost, "/api/writing/case/case-1/reviews", map[string]any{})
	testutil.MustStatus(t, http.StatusConflict, status, body)
	if code := errorCode(t, body); code != "no_document" {
		t.Fatalf("error code = %q, want no_document", code)
	}

	// Anonymous callers are rejected before path validation.
	anonymous := env.newClient(t)
	status, body = anonymous.Do(t, http.MethodGet, "/api/writing/case/case-1", nil)
	testutil.MustStatus(t, http.StatusUnauthorized, status, body)
}

func TestConflictKeepsBothSides(t *testing.T) {
	env := newTestEnv(t)
	insertSubjectiveItem(t, env.db, "case-1", "case")
	env.createUser("writer@example.com")
	env.login(env.server, "writer@example.com")

	status, body := env.putDocument(env.server, "case", "case-1", map[string]string{"answer-0": "版本一"}, 0)
	testutil.MustStatus(t, http.StatusOK, status, body)
	documentID := stringValue(documentOf(t, body), "id")

	status, body = env.putDocument(env.server, "case", "case-1", map[string]string{"answer-0": "版本二"}, 1)
	testutil.MustStatus(t, http.StatusOK, status, body)

	// A stale client (still on version 1) must not overwrite version 2.
	status, body = env.putDocument(env.server, "case", "case-1", map[string]string{"answer-0": "客户端的版本三"}, 1)
	testutil.MustStatus(t, http.StatusConflict, status, body)
	if code := errorCode(t, body); code != "conflict" {
		t.Fatalf("error code = %q, want conflict", code)
	}
	details := detailsOf(t, body)
	if got := numberValue(t, details, "serverVersion"); got != 2 {
		t.Fatalf("serverVersion = %d, want 2", got)
	}
	serverValues := objectValue(t, details["serverValues"], "serverValues")
	clientValues := objectValue(t, details["clientValues"], "clientValues")
	if serverValues["answer-0"] != "版本二" {
		t.Fatalf("serverValues = %v", serverValues)
	}
	if clientValues["answer-0"] != "客户端的版本三" {
		t.Fatalf("clientValues = %v", clientValues)
	}

	// The server document is untouched by the rejected write.
	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-1", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	doc := documentOf(t, body)
	if numberValue(t, doc, "version") != 2 {
		t.Fatalf("document version = %d, want 2", numberValue(t, doc, "version"))
	}
	if objectValue(t, doc["values"], "document.values")["answer-0"] != "版本二" {
		t.Fatalf("conflicting write silently overwrote the document: %v", doc["values"])
	}

	// Both sides are preserved as conflict snapshots.
	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-1/versions", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items := arrayValue(t, body["items"], "items")
	if len(items) != 4 {
		t.Fatalf("version count = %d, want 4 (2 user + 2 conflict): %v", len(items), items)
	}
	found := map[string]bool{}
	for _, raw := range items {
		item := objectValue(t, raw, "version item")
		found[stringValue(item, "source")] = true
	}
	if !found["user"] || !found["conflict"] {
		t.Fatalf("version sources = %v, want user and conflict", found)
	}

	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-1/versions/2?source=conflict", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	conflictServer := objectValue(t, body["version"], "version")
	if stringValue(conflictServer, "source") != "conflict" {
		t.Fatalf("conflict version source = %v", conflictServer)
	}
	if objectValue(t, conflictServer["values"], "values")["answer-0"] != "版本二" {
		t.Fatalf("conflict server snapshot = %v", conflictServer["values"])
	}

	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-1/versions/1?source=conflict", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	conflictClient := objectValue(t, body["version"], "version")
	if objectValue(t, conflictClient["values"], "values")["answer-0"] != "客户端的版本三" {
		t.Fatalf("conflict client snapshot = %v", conflictClient["values"])
	}

	// The regular user history is still intact.
	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-1/versions/1", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if objectValue(t, objectValue(t, body["version"], "version")["values"], "values")["answer-0"] != "版本一" {
		t.Fatalf("user version 1 was replaced: %v", body["version"])
	}

	var conflictCount int
	if err := env.db.Pool.QueryRow(t.Context(),
		`select count(*) from writing_versions where document_id = $1::uuid and source = 'conflict'`,
		documentID).Scan(&conflictCount); err != nil {
		t.Fatalf("count conflict versions: %v", err)
	}
	if conflictCount != 2 {
		t.Fatalf("conflict version rows = %d, want 2", conflictCount)
	}
}

func TestPermissionIsolationBetweenUsers(t *testing.T) {
	env := newTestEnv(t)
	insertSubjectiveItem(t, env.db, "case-1", "case")
	env.createUser("alice@example.com")
	env.createUser("bob@example.com")

	aliceServer := env.newClient(t)
	env.login(aliceServer, "alice@example.com")
	status, body := env.putDocument(aliceServer, "case", "case-1", map[string]string{
		"answer-0": "alice 的私有答案",
	}, 0)
	testutil.MustStatus(t, http.StatusOK, status, body)
	status, body = aliceServer.Do(t, http.MethodPost, "/api/writing/case/case-1/reviews", map[string]any{})
	testutil.MustStatus(t, http.StatusCreated, status, body)

	// Bob can reach the shared target but sees an empty draft, no history and
	// no reviews of Alice's document.
	bobServer := env.newClient(t)
	env.login(bobServer, "bob@example.com")
	status, body = bobServer.Do(t, http.MethodGet, "/api/writing/case/case-1", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	bobDoc := documentOf(t, body)
	if numberValue(t, bobDoc, "version") != 0 || len(objectValue(t, bobDoc["values"], "values")) != 0 {
		t.Fatalf("bob sees alice's document: %v", bobDoc)
	}
	status, body = bobServer.Do(t, http.MethodGet, "/api/writing/case/case-1/versions", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if len(arrayValue(t, body["items"], "items")) != 0 {
		t.Fatalf("bob sees alice's versions: %v", body)
	}
	status, body = bobServer.Do(t, http.MethodGet, "/api/writing/case/case-1/reviews", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if len(arrayValue(t, body["items"], "items")) != 0 {
		t.Fatalf("bob sees alice's reviews: %v", body)
	}

	// Bob's own draft is separate and does not affect Alice.
	status, body = env.putDocument(bobServer, "case", "case-1", map[string]string{
		"answer-0": "bob 的答案",
	}, 0)
	testutil.MustStatus(t, http.StatusOK, status, body)

	status, body = aliceServer.Do(t, http.MethodGet, "/api/writing/case/case-1", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	aliceDoc := documentOf(t, body)
	if objectValue(t, aliceDoc["values"], "values")["answer-0"] != "alice 的私有答案" {
		t.Fatalf("alice document changed: %v", aliceDoc)
	}
	status, body = aliceServer.Do(t, http.MethodGet, "/api/writing/case/case-1/reviews", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if len(arrayValue(t, body["items"], "items")) != 1 {
		t.Fatalf("alice reviews = %v, want 1", body)
	}

	var documents int
	if err := env.db.Pool.QueryRow(t.Context(),
		`select count(*) from writing_documents where kind = 'case' and target_id = 'case-1'`).Scan(&documents); err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if documents != 2 {
		t.Fatalf("documents = %d, want 2 (one per user)", documents)
	}
}

// TestRoutesRegisteredInApplication checks the real assembly registers the
// writing routes, not just the package-local test handler.
func TestRoutesRegisteredInApplication(t *testing.T) {
	application, server, _ := testutil.NewApp(t)
	testutil.CreateUser(t, application, "wired@example.com", password, "learner")
	testutil.Login(t, server, "wired@example.com", password)

	status, body := server.Do(t, http.MethodGet, "/api/writing/case/does-not-exist", nil)
	testutil.MustStatus(t, http.StatusNotFound, status, body)
	status, body = server.Do(t, http.MethodGet, "/api/writing/unknown/does-not-exist", nil)
	testutil.MustStatus(t, http.StatusBadRequest, status, body)
}

func TestConcurrentWritesKeepOneWinnerAndConflictSnapshots(t *testing.T) {
	env := newTestEnv(t)
	insertSubjectiveItem(t, env.db, "case-1", "case")
	insertSubjectiveItem(t, env.db, "case-2", "case")
	env.createUser("writer@example.com")
	env.login(env.server, "writer@example.com")

	status, body := env.putDocument(env.server, "case", "case-1", map[string]string{"answer-0": "基础版本"}, 0)
	testutil.MustStatus(t, http.StatusOK, status, body)

	first := env.newClient(t)
	second := env.newClient(t)
	env.login(first, "writer@example.com")
	env.login(second, "writer@example.com")

	runConcurrentPut := func(t *testing.T, targetID string, version int) []int {
		t.Helper()
		type writeResult struct {
			status int
			err    error
		}
		results := make(chan writeResult, 2)
		var wg sync.WaitGroup
		for index, client := range []*testutil.Server{first, second} {
			wg.Add(1)
			go func(index int, server *testutil.Server) {
				defer wg.Done()
				status, _, err := doJSONRaw(server.Client, http.MethodPut,
					server.URL+"/api/writing/case/"+targetID,
					map[string]any{
						"values":  map[string]string{"answer-0": fmt.Sprintf("并发写入 %d", index)},
						"version": version,
					})
				results <- writeResult{status: status, err: err}
			}(index, client)
		}
		wg.Wait()
		close(results)
		var statuses []int
		for result := range results {
			if result.err != nil {
				t.Fatalf("concurrent PUT %s: %v", targetID, result.err)
			}
			statuses = append(statuses, result.status)
		}
		sort.Ints(statuses)
		return statuses
	}

	statuses := runConcurrentPut(t, "case-1", 1)
	if len(statuses) != 2 || statuses[0] != http.StatusOK || statuses[1] != http.StatusConflict {
		t.Fatalf("concurrent statuses = %v, want [200 409]", statuses)
	}

	// The winner's document survives; the loser is kept as a conflict pair.
	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-1", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if got := numberValue(t, documentOf(t, body), "version"); got != 2 {
		t.Fatalf("document version after concurrent writes = %d, want 2", got)
	}
	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-1/versions", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items := arrayValue(t, body["items"], "items")
	conflicts := 0
	for _, raw := range items {
		if stringValue(objectValue(t, raw, "version item"), "source") == "conflict" {
			conflicts++
		}
	}
	if len(items) != 4 || conflicts != 2 {
		t.Fatalf("versions after conflict = %d rows / %d conflict, want 4/2: %v", len(items), conflicts, items)
	}

	// Two devices creating the same brand-new document also resolve to one
	// winner plus preserved conflict snapshots.
	statuses = runConcurrentPut(t, "case-2", 0)
	if len(statuses) != 2 || statuses[0] != http.StatusOK || statuses[1] != http.StatusConflict {
		t.Fatalf("concurrent create statuses = %v, want [200 409]", statuses)
	}
	status, body = env.server.Do(t, http.MethodGet, "/api/writing/case/case-2", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if got := numberValue(t, documentOf(t, body), "version"); got != 1 {
		t.Fatalf("created document version = %d, want 1", got)
	}
	var createConflicts int
	if err := env.db.Pool.QueryRow(t.Context(), `
		select count(*) from writing_versions v
		join writing_documents d on d.id = v.document_id
		where d.target_id = 'case-2' and v.source = 'conflict'`).Scan(&createConflicts); err != nil {
		t.Fatalf("count create conflicts: %v", err)
	}
	if createConflicts != 2 {
		t.Fatalf("create conflict snapshots = %d, want 2", createConflicts)
	}
}

func TestDistinctConflictsAtSameVersionRemainReadable(t *testing.T) {
	env := newTestEnv(t)
	insertSubjectiveItem(t, env.db, "conflict-many", "essay")
	env.createUser("many-conflicts@example.com")
	env.login(env.server, "many-conflicts@example.com")
	for version := 0; version < 2; version++ {
		status, body := env.putDocument(env.server, "essay", "conflict-many", map[string]string{"body": fmt.Sprint(version)}, version)
		testutil.MustStatus(t, http.StatusOK, status, body)
	}
	for _, text := range []string{"offline A", "offline B", "offline A"} {
		status, body := env.putDocument(env.server, "essay", "conflict-many", map[string]string{"body": text}, 1)
		testutil.MustStatus(t, http.StatusConflict, status, body)
	}
	status, body := env.server.Do(t, http.MethodGet, "/api/writing/essay/conflict-many/versions", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	found := map[string]bool{}
	for _, raw := range body["items"].([]any) {
		item := raw.(map[string]any)
		if item["source"] != "conflict" || item["version"] != float64(1) {
			continue
		}
		path := "/api/writing/essay/conflict-many/versions/1?source=conflict&snapshotId=" + item["id"].(string)
		code, detail := env.server.Do(t, http.MethodGet, path, nil)
		testutil.MustStatus(t, http.StatusOK, code, detail)
		value := detail["version"].(map[string]any)["values"].(map[string]any)["body"].(string)
		if found[value] {
			t.Fatalf("identical replay created another conflict: %s", value)
		}
		found[value] = true
	}
	if len(found) != 2 || !found["offline A"] || !found["offline B"] {
		t.Fatalf("lost conflict: %v", found)
	}
}

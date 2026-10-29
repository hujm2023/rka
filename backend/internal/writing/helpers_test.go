package writing_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"rka/internal/ai"
	"rka/internal/audit"
	"rka/internal/auth"
	"rka/internal/httpx"
	"rka/internal/store"
	"rka/internal/testutil"
	"rka/internal/writing"
)

const password = "correct-horse-battery"

// newUUID builds a v4 UUID without adding a dependency to go.mod.
func newUUID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

// fakeTaskCreator records every task request so tests can assert the exact
// binding (kind, idempotency key, context) without a model gateway.
type fakeTaskCreator struct {
	mu     sync.Mutex
	calls  []ai.CreateTaskInput
	id     string
	status string
	err    error
}

func (f *fakeTaskCreator) CreateTask(_ context.Context, in ai.CreateTaskInput) (ai.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, in)
	if f.err != nil {
		return ai.Task{}, f.err
	}
	id := f.id
	if id == "" {
		id = newUUID()
	}
	status := f.status
	if status == "" {
		status = "queued"
	}
	return ai.Task{
		ID:        id,
		Kind:      in.Kind,
		Status:    status,
		Context:   in.Context,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (f *fakeTaskCreator) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeTaskCreator) call(index int) ai.CreateTaskInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[index]
}

// testEnv builds the writing service with the same middleware chain the real
// application uses, but with a controllable TaskCreator.
type testEnv struct {
	t       *testing.T
	db      *store.DB
	auth    *auth.Service
	ai      *fakeTaskCreator
	handler http.Handler
	server  *testutil.Server
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	cfg := testutil.Config(t)
	db := testutil.NewDatabase(t)
	log := testutil.Logger()
	authSvc := auth.New(cfg, db, log, audit.New(db, log))
	creator := &fakeTaskCreator{}
	svc := writing.New(writing.Deps{Config: cfg, DB: db, Log: log, Auth: authSvc, AI: creator})
	mux := http.NewServeMux()
	authSvc.Register(mux)
	svc.Register(mux)
	handler := httpx.Chain(mux,
		httpx.Recover(log),
		httpx.RequestID,
		httpx.SecurityHeaders,
		httpx.SameOrigin,
		authSvc.Middleware,
	)
	return &testEnv{
		t:       t,
		db:      db,
		auth:    authSvc,
		ai:      creator,
		handler: handler,
		server:  testutil.NewServer(t, handler),
	}
}

// newClient returns another browser context with its own cookie jar, which is
// how cross-device and cross-account behaviour is exercised.
func (e *testEnv) newClient(t *testing.T) *testutil.Server {
	t.Helper()
	return testutil.NewServer(t, e.handler)
}

func (e *testEnv) createUser(email string) auth.User {
	e.t.Helper()
	user, err := e.auth.CreateUser(e.t.Context(), email, password, "", "learner")
	if err != nil {
		e.t.Fatalf("create user %s: %v", email, err)
	}
	return user
}

func (e *testEnv) login(server *testutil.Server, email string) auth.User {
	e.t.Helper()
	status, body := server.Do(e.t, http.MethodPost, "/api/auth/login", map[string]any{
		"email": email, "password": password,
	})
	testutil.MustStatus(e.t, http.StatusOK, status, body)
	user, _ := body["user"].(map[string]any)
	return auth.User{ID: stringValue(user, "id"), Email: email}
}

// insertSubjectiveItem registers a writing target the way the content importer
// would.
func insertSubjectiveItem(t *testing.T, db *store.DB, id, kind string) {
	t.Helper()
	_, err := db.Pool.Exec(t.Context(), `
		insert into subjective_items (id, kind, revision, title, module, term, source_type, payload)
		values ($1, $2, 'rev-1', '测试写作题', 'architecture', '2026年5月', 'mock', '{}'::jsonb)`,
		id, kind)
	if err != nil {
		t.Fatalf("insert subjective item %s: %v", id, err)
	}
}

func (e *testEnv) putDocument(server *testutil.Server, kind, targetID string, values map[string]string, version int) (int, map[string]any) {
	e.t.Helper()
	return server.Do(e.t, http.MethodPut, "/api/writing/"+kind+"/"+targetID, map[string]any{
		"values": values, "version": version,
	})
}

// ---------------------------------------------------------------- assertions

// doJSONRaw is the goroutine-safe variant of testutil.Server.Do: it never calls
// t.Fatalf, so concurrent-request tests can collect both outcomes.
func doJSONRaw(client *http.Client, method, url string, body any) (int, map[string]any, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return response.StatusCode, nil, err
	}
	decoded := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return response.StatusCode, nil, err
		}
	}
	return response.StatusCode, decoded, nil
}

func objectValue(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	obj, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s = %T, want JSON object", label, value)
	}
	return obj
}

func arrayValue(t *testing.T, value any, label string) []any {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("%s = %T, want JSON array", label, value)
	}
	return items
}

func stringValue(obj map[string]any, key string) string {
	value, _ := obj[key].(string)
	return value
}

func numberValue(t *testing.T, obj map[string]any, key string) int {
	t.Helper()
	value, ok := obj[key].(float64)
	if !ok {
		t.Fatalf("%s = %v (%T), want number", key, obj[key], obj[key])
	}
	return int(value)
}

func errorObject(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	return objectValue(t, body["error"], "error")
}

func errorCode(t *testing.T, body map[string]any) string {
	t.Helper()
	return stringValue(errorObject(t, body), "code")
}

func detailsOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	return objectValue(t, errorObject(t, body)["details"], "error.details")
}

func documentOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	return objectValue(t, body["document"], "document")
}

func reviewOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	return objectValue(t, body["review"], "review")
}

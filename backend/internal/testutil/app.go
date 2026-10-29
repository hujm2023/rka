package testutil

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"rka/internal/app"
	"rka/internal/auth"
	"rka/internal/config"
	"rka/internal/store"
)

func newJar() http.CookieJar {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic(err)
	}
	return jar
}

// NewApp builds the full application against an isolated database.
func NewApp(t *testing.T) (*app.App, *Server, *store.DB) {
	t.Helper()
	return NewAppWithConfig(t, Config(t))
}

// NewAppWithConfig builds the application with a caller-provided configuration.
func NewAppWithConfig(t *testing.T, cfg *config.Config) (*app.App, *Server, *store.DB) {
	t.Helper()
	db := NewDatabase(t)
	application, err := app.New(t.Context(), cfg, db, Logger())
	if err != nil {
		t.Fatalf("build application: %v", err)
	}
	server := NewServer(t, application.Handler)
	return application, server, db
}

// NewAppFor starts another browser-facing server for an existing application,
// which is how cross-device behaviour is tested (each server has its own
// cookie jar).
func NewAppFor(t *testing.T, application *app.App) (*app.App, *Server, *store.DB) {
	t.Helper()
	server := NewServer(t, application.Handler)
	return application, server, application.DB
}

// ContentDir locates the bundled content snapshot from any package directory.
func ContentDir(t *testing.T) string {
	t.Helper()
	if value := os.Getenv("RKA_TEST_CONTENT_DIR"); value != "" {
		return value
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository root")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, "content")
		if _, err := os.Stat(filepath.Join(candidate, "catalog.json")); err == nil {
			return candidate
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("content/catalog.json not found")
	return ""
}

// Do performs a JSON request and returns the status and decoded body.
func (s *Server) Do(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, s.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := s.Client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	decoded := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("%s %s: response is not JSON: %s", method, path, string(raw))
		}
	}
	return response.StatusCode, decoded
}

// MustStatus fails the test when the status does not match.
func MustStatus(t *testing.T, want, got int, body map[string]any) map[string]any {
	t.Helper()
	if got != want {
		t.Fatalf("status = %d, want %d; body=%v", got, want, body)
	}
	return body
}

// CreateUser inserts an account and returns it.
func CreateUser(t *testing.T, application *app.App, email, password, role string) auth.User {
	t.Helper()
	user, err := application.Auth.CreateUser(t.Context(), email, password, "", role)
	if err != nil {
		t.Fatalf("create user %s: %v", email, err)
	}
	return user
}

// Login signs the server's cookie jar in as the given account.
func Login(t *testing.T, server *Server, email, password string) auth.User {
	t.Helper()
	status, body := server.Do(t, http.MethodPost, "/api/auth/login", map[string]any{"email": email, "password": password})
	MustStatus(t, http.StatusOK, status, body)
	user, _ := body["user"].(map[string]any)
	id, _ := user["id"].(string)
	return auth.User{ID: id, Email: email}
}

// ImportContent imports the bundled snapshot once for the test database.
func ImportContent(t *testing.T, application *app.App) {
	t.Helper()
	result, err := application.Content.ImportSnapshot(t.Context(), ContentDir(t), "")
	if err != nil {
		t.Fatalf("import content: %v", err)
	}
	if result.Version == "" {
		t.Fatalf("import returned empty version: %+v", result)
	}
}

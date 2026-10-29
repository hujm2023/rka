// Package testutil provides isolated PostgreSQL databases and HTTP test
// servers. Integration tests exercise the public HTTP boundary, not internal
// function calls, and each test gets its own database so parallel packages
// cannot interfere.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"rka/internal/config"
	"rka/internal/store"
)

// BaseDatabaseURL is the maintenance connection used to create test databases.
// Default points at the development Compose database on 127.0.0.1:55432.
func BaseDatabaseURL() string {
	if value := strings.TrimSpace(os.Getenv("RKA_TEST_DATABASE_URL")); value != "" {
		return value
	}
	return "postgres://rka:devpassword@127.0.0.1:55432/postgres?sslmode=disable"
}

// NewDatabase creates an empty database, applies migrations, and drops it when
// the test ends.
func NewDatabase(t *testing.T) *store.DB {
	t.Helper()
	base := BaseDatabaseURL()
	adminURL, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse RKA_TEST_DATABASE_URL: %v", err)
	}
	name := "rka_test_" + randomSuffix()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Skipf("PostgreSQL unavailable at %s: %v (start it with scripts/dev-db.sh)", base, err)
	}
	if _, err := admin.Exec(ctx, "create database "+name); err != nil {
		admin.Close(ctx)
		t.Fatalf("create test database: %v", err)
	}
	admin.Close(ctx)
	testURL := *adminURL
	testURL.Path = "/" + name
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(ctx, testURL.String(), log)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if _, err := db.Migrate(ctx); err != nil {
		db.Close()
		t.Fatalf("migrate test database: %v", err)
	}
	t.Cleanup(func() {
		db.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		conn, err := pgx.Connect(cleanupCtx, base)
		if err != nil {
			return
		}
		defer conn.Close(cleanupCtx)
		_, _ = conn.Exec(cleanupCtx, "drop database if exists "+name+" with (force)")
	})
	return db
}

// Config returns a test configuration rooted in a temporary directory.
func Config(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Mode:         "test",
		Addr:         "127.0.0.1:0",
		DataDir:      dir,
		StaticDir:    dir,
		SessionTTL:   time.Hour,
		CookieName:   "rka_session",
		CookieSecure: "never",
		LogLevel:     "error",
		LogFormat:    "text",
		SecretKey:    make([]byte, 32),
		Worker: config.WorkerConfig{
			Enabled:        false,
			ID:             "test-worker",
			PollInterval:   10 * time.Millisecond,
			LeaseTTL:       time.Minute,
			Heartbeat:      10 * time.Second,
			MaxAttempts:    3,
			MaxConcurrency: 1,
		},
		AI: config.AIConfig{MaxConcurrency: 3, RequestTimeout: 5 * time.Second},
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("ensure data dirs: %v", err)
	}
	return cfg
}

// Logger returns a quiet logger for tests.
func Logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// Server wraps httptest.Server with a cookie-aware client.
type Server struct {
	*httptest.Server
	Client *http.Client
}

// NewServer starts the HTTP surface. The handler is built by the caller so each
// package can wire only the services it needs.
func NewServer(t *testing.T, handler http.Handler) *Server {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	jar := newJar()
	return &Server{Server: ts, Client: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

func randomSuffix() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

package ops_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"rka/internal/app"
	"rka/internal/config"
	"rka/internal/store"
	"rka/internal/testutil"
)

// pgTool finds a PostgreSQL client binary the same way the implementation
// does: explicit override, PATH, then common installation directories.
func pgTool(t *testing.T, envKey, name string) string {
	t.Helper()
	if candidate := strings.TrimSpace(os.Getenv(envKey)); candidate != "" {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	if resolved, err := exec.LookPath(name); err == nil {
		return resolved
	}
	for _, pattern := range []string{"/usr/lib/postgresql/*/bin/" + name, "/usr/local/pgsql/bin/" + name} {
		matches, _ := filepath.Glob(pattern)
		for _, match := range matches {
			if info, err := os.Stat(match); err == nil && !info.IsDir() {
				return match
			}
		}
	}
	return ""
}

func requirePgTools(t *testing.T) {
	t.Helper()
	dump := pgTool(t, "RKA_PG_DUMP", "pg_dump")
	restore := pgTool(t, "RKA_PG_RESTORE", "pg_restore")
	if dump == "" || restore == "" {
		t.Skip("pg_dump/pg_restore 在宿主机与 PATH 中均不可用；按任务约定跳过真实备份/恢复测试并记录缺口（容器运行镜像 postgres:18.6-alpine 自带这两个工具）")
	}
}

func databaseURLFor(t *testing.T, database string) string {
	t.Helper()
	parsed, err := url.Parse(testutil.BaseDatabaseURL())
	if err != nil {
		t.Fatalf("parse base database url: %v", err)
	}
	parsed.Path = "/" + database
	return parsed.String()
}

// newBackupApp builds the full application on an existing isolated test
// database, with cfg.DatabaseURL pointing at that exact database so pg_dump
// and pg_restore can operate on it.
func newBackupApp(t *testing.T) (*app.App, *testutil.Server, *store.DB, *config.Config) {
	t.Helper()
	db := testutil.NewDatabase(t)
	var database string
	if err := db.Pool.QueryRow(context.Background(), `select current_database()`).Scan(&database); err != nil {
		t.Fatalf("current database: %v", err)
	}
	cfg := testutil.Config(t)
	cfg.DatabaseURL = databaseURLFor(t, database)
	application, err := app.New(context.Background(), cfg, db, testutil.Logger())
	if err != nil {
		t.Fatalf("build application: %v", err)
	}
	server := testutil.NewServer(t, application.Handler)
	return application, server, db, cfg
}

func TestBackupVerifyRestoreRoundTrip(t *testing.T) {
	requirePgTools(t)
	application, server, db, cfg := newBackupApp(t)
	testutil.CreateUser(t, application, "admin@example.com", password, "admin")
	testutil.Login(t, server, "admin@example.com", password)
	insertQuestion(t, db, "q-backup", "rev1", "C")
	insertMaterial(t, db, "m-backup")

	// One immutable original referenced by the blobs table.
	content := []byte("# 原始资料\n\n内容寻址原件。\n")
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	originalRel := filepath.Join("materials", sha+".md")
	originalPath := filepath.Join(cfg.OriginalsDir(), originalRel)
	if err := os.MkdirAll(filepath.Dir(originalPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(originalPath, content, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into blobs (sha256, size, rel_path) values ($1, $2, $3)`,
		sha, len(content), filepath.ToSlash(originalRel)); err != nil {
		t.Fatal(err)
	}
	// The application secret key must travel with the batch so a restored data
	// root can decrypt model credentials and teaching identities.
	secretContent := []byte(hex.EncodeToString(cfg.SecretKey) + "\n")
	if err := os.WriteFile(filepath.Join(cfg.SecretsDir(), "secret-key"), secretContent, 0o600); err != nil {
		t.Fatal(err)
	}

	status, body := server.Do(t, http.MethodPost, "/api/admin/backups", nil)
	testutil.MustStatus(t, http.StatusCreated, status, body)
	backup, _ := body["backup"].(map[string]any)
	id, _ := backup["id"].(string)
	path, _ := backup["path"].(string)
	if id == "" || path == "" {
		t.Fatalf("backup response incomplete: %v", body)
	}
	for _, name := range []string{"db.dump", "manifest.json", "RESTORE.md"} {
		if _, err := os.Stat(filepath.Join(path, name)); err != nil {
			t.Fatalf("backup missing %s: %v", name, err)
		}
	}
	backupOriginal := filepath.Join(path, "originals", originalRel)
	raw, err := os.ReadFile(backupOriginal)
	if err != nil || string(raw) != string(content) {
		t.Fatalf("backup original missing or changed: %v", err)
	}
	backupSecret, err := os.ReadFile(filepath.Join(path, "secrets", "secret-key"))
	if err != nil || string(backupSecret) != string(secretContent) {
		t.Fatalf("backup does not contain the application secret key: %v", err)
	}
	secretInfo, _ := os.Stat(filepath.Join(path, "secrets", "secret-key"))
	if secretInfo == nil || secretInfo.Mode().Perm() != 0o600 {
		t.Fatalf("backup secret key permissions = %v, want 0600", secretInfo.Mode().Perm())
	}
	manifestRaw, err := os.ReadFile(filepath.Join(path, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	if manifest["schemaVersion"] != float64(1) || manifest["app"] != "rka" {
		t.Fatalf("unexpected manifest identity: %v", manifest["schemaVersion"])
	}
	if manifest["binaryVersion"] == nil || manifest["dbDumpSha256"] == nil {
		t.Fatalf("manifest missing version/digest: %v", manifest)
	}
	tables, _ := manifest["tables"].(map[string]any)
	if tables["users"] == nil || tables["questions"] == nil || tables["attempts"] == nil {
		t.Fatalf("manifest missing table counts: %v", tables)
	}
	originals, _ := manifest["originals"].([]any)
	if len(originals) != 1 {
		t.Fatalf("manifest originals = %v", originals)
	}
	secretsManifest, _ := manifest["secrets"].(map[string]any)
	if secretsManifest["included"] != true || secretsManifest["sha256"] == nil {
		t.Fatalf("manifest secrets metadata incomplete: %v", secretsManifest)
	}
	dumpSum, err := os.ReadFile(filepath.Join(path, "db.dump"))
	if err != nil {
		t.Fatal(err)
	}
	dumpHash := sha256.Sum256(dumpSum)
	if manifest["dbDumpSha256"] != hex.EncodeToString(dumpHash[:]) {
		t.Fatalf("manifest dbDumpSha256 does not match db.dump")
	}

	status, body = server.Do(t, http.MethodGet, "/api/admin/backups", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	listItems, _ := body["items"].([]any)
	if len(listItems) == 0 {
		t.Fatalf("backup list is empty")
	}
	listed, _ := listItems[0].(map[string]any)
	if listed["id"] != id || listed["dbDumpSha256"] == "" || listed["dbDumpSha256"] == nil {
		t.Fatalf("backup list projection incomplete: %v", listed)
	}

	// Verification performs a real pg_restore into a temporary database.
	status, body = server.Do(t, http.MethodPost, "/api/admin/backups/"+id+"/verify", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	report, _ := body["report"].(map[string]any)
	if report["ok"] != true {
		t.Fatalf("verify failed: %v", report)
	}
	checks, _ := report["checks"].([]any)
	if len(checks) == 0 {
		t.Fatalf("verify returned no checks")
	}
	errorsList, _ := report["errors"].([]any)
	if len(errorsList) != 0 {
		t.Fatalf("verify errors: %v", errorsList)
	}
	warnings, _ := report["warnings"].([]any)
	if len(warnings) != 0 {
		t.Fatalf("verify warnings: %v", warnings)
	}
	chekcsJoined := ""
	for _, item := range checks {
		chekcsJoined += item.(string)
	}
	if !strings.Contains(chekcsJoined, "secret-key") {
		t.Fatalf("verify checks should cover the application secret key: %v", checks)
	}

	// Restore writes only the new target directory.
	target := filepath.Join(t.TempDir(), "restore-target")
	status, body = server.Do(t, http.MethodPost, "/api/admin/backups/"+id+"/restore", map[string]any{"targetDir": target})
	testutil.MustStatus(t, http.StatusOK, status, body)
	for _, name := range []string{"db.dump", "manifest.json", "RESTORE.md"} {
		if _, err := os.Stat(filepath.Join(target, name)); err != nil {
			t.Fatalf("restore target missing %s: %v", name, err)
		}
	}
	for _, dir := range []string{"originals", "derived", "exports", "teaching", "cache", "gateway", "logs", "secrets", "backups", "postgres"} {
		if info, err := os.Stat(filepath.Join(target, dir)); err != nil || !info.IsDir() {
			t.Fatalf("restore target missing directory %s: %v", dir, err)
		}
	}
	restoredOriginal, err := os.ReadFile(filepath.Join(target, "originals", originalRel))
	if err != nil || string(restoredOriginal) != string(content) {
		t.Fatalf("restored original changed: %v", err)
	}
	restoredSecret, err := os.ReadFile(filepath.Join(target, "secrets", "secret-key"))
	if err != nil || string(restoredSecret) != string(secretContent) {
		t.Fatalf("restored data root lost the application secret key: %v", err)
	}
	restoredSecretInfo, statErr := os.Stat(filepath.Join(target, "secrets", "secret-key"))
	if statErr != nil || restoredSecretInfo.Mode().Perm() != 0o600 {
		t.Fatalf("restored secret key permissions wrong: %v", statErr)
	}
	restoreBody, _ := body["restore"].(map[string]any)
	if restoreBody["secretsIncluded"] != true {
		t.Fatalf("restore result should report secretsIncluded: %v", restoreBody)
	}
	readme, _ := os.ReadFile(filepath.Join(target, "RESTORE.md"))
	if !strings.Contains(string(readme), "pg_restore") || !strings.Contains(string(readme), "RKA_DATA_DIR") ||
		!strings.Contains(string(readme), "RKA_SECRET_KEY_FILE") || !strings.Contains(string(readme), "敏感") {
		t.Fatalf("RESTORE.md missing restore/secret instructions: %s", string(readme))
	}

	// A non-empty target must be refused.
	status, body = server.Do(t, http.MethodPost, "/api/admin/backups/"+id+"/restore", map[string]any{"targetDir": target})
	testutil.MustStatus(t, http.StatusConflict, status, body)

	// The live data root was not overwritten.
	var users int
	if err := db.Pool.QueryRow(t.Context(), `select count(*) from users`).Scan(&users); err != nil || users == 0 {
		t.Fatalf("live database changed: users=%d err=%v", users, err)
	}
}

func TestBackupWithoutPgDumpFailsExplicitly(t *testing.T) {
	application, server, _ := testutil.NewApp(t)
	testutil.CreateUser(t, application, "admin@example.com", password, "admin")
	testutil.Login(t, server, "admin@example.com", password)
	t.Setenv("RKA_PG_DUMP", "/nonexistent/rka-pg_dump")
	status, body := server.Do(t, http.MethodPost, "/api/admin/backups", nil)
	testutil.MustStatus(t, http.StatusInternalServerError, status, body)
	message, _ := body["error"].(map[string]any)["message"].(string)
	if !strings.Contains(message, "pg_dump") {
		t.Fatalf("error should name the missing pg_dump: %q", message)
	}
	var failures int
	if err := application.DB.Pool.QueryRow(t.Context(), `select count(*) from backups where status = 'failed'`).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures == 0 {
		t.Fatalf("failed backup attempt should be visible in the backups table")
	}
}

func TestVerifyBackupWithoutPgRestoreReportsExplicitError(t *testing.T) {
	application, server, _, cfg := newBackupApp(t)
	testutil.CreateUser(t, application, "admin@example.com", password, "admin")
	testutil.Login(t, server, "admin@example.com", password)
	t.Setenv("RKA_PG_RESTORE", "/nonexistent/rka-pg_restore")

	// Build a syntactically valid batch with an empty dump so verification
	// reaches the missing pg_restore step instead of failing on hashes.
	dir := filepath.Join(cfg.BackupDir(), "20200101T000000Z")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	dump := []byte("not-a-real-dump")
	if err := os.WriteFile(filepath.Join(dir, "db.dump"), dump, 0o640); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(dump)
	manifest := map[string]any{
		"app": "rka", "schemaVersion": 1,
		"dbDump":       map[string]any{"file": "db.dump", "size": len(dump), "sha256": hex.EncodeToString(sum[:])},
		"dbDumpSha256": hex.EncodeToString(sum[:]),
		"tables":       map[string]any{},
		"originals":    []any{},
	}
	payload, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), payload, 0o640); err != nil {
		t.Fatal(err)
	}

	status, body := server.Do(t, http.MethodPost, "/api/admin/backups/20200101T000000Z/verify", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	report, _ := body["report"].(map[string]any)
	if report["ok"] != false {
		t.Fatalf("verification without pg_restore must not report success: %v", report)
	}
	errorsList, _ := report["errors"].([]any)
	if len(errorsList) == 0 {
		t.Fatalf("missing pg_restore should produce an explicit error")
	}
	joined := ""
	for _, item := range errorsList {
		joined += item.(string)
	}
	if !strings.Contains(joined, "pg_restore") {
		t.Fatalf("error should name pg_restore: %s", joined)
	}
	warnings, _ := report["warnings"].([]any)
	if len(warnings) == 0 {
		t.Fatalf("a batch without the app secret key should produce a warning, not a silent pass")
	}
}

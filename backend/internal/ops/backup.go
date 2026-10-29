package ops

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"rka/internal/httpx"
	"rka/internal/store"
)

const (
	backupFormatVersion = 1
	dbDumpFileName      = "db.dump"
	manifestFileName    = "manifest.json"
	restoreReadmeName   = "RESTORE.md"
	originalsDirName    = "originals"
	secretKeyRelPath    = "secrets/secret-key"
	backupTimestampFmt  = "20060102T150405Z"
)

var backupTimestampPattern = regexp.MustCompile(`^\d{8}T\d{6}Z$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// backupTables is the fixed representative set whose row counts are recorded in
// the manifest and re-checked after a restore rehearsal. The names are
// constants, never user input.
var backupTables = []string{
	"platform_state", "users", "invites", "sessions",
	"blobs", "materials", "material_versions", "material_sections",
	"knowledge_indexes", "questions", "question_versions", "question_knowledge_points",
	"knowledge_points", "knowledge_links", "subjective_items", "content_lessons",
	"practice_sessions", "attempts", "marks",
	"reading_progress", "annotations", "evidence", "mastery", "review_items",
	"writing_documents", "writing_versions", "writing_reviews", "teaching_identities",
	"ai_tasks", "ai_invocations", "ai_events", "ai_messages", "model_configs", "content_grants",
	"settings", "audit_log", "jobs", "backups", "imported_records",
}

// backupManifest is the on-disk contract of one backup batch. The field names
// are part of the operator-facing format: verify and restore read this file
// without consulting the database.
type backupManifest struct {
	Components      map[string][]backupFile `json:"components,omitempty"`
	App             string                  `json:"app"`
	SchemaVersion   int                     `json:"schemaVersion"`
	BackupID        string                  `json:"backupId,omitempty"`
	CreatedAt       time.Time               `json:"createdAt"`
	CreatedBy       string                  `json:"createdBy,omitempty"`
	BinaryVersion   int64                   `json:"binaryVersion"`
	DBSchemaVersion int64                   `json:"dbSchemaVersion,omitempty"`
	ContentVersion  string                  `json:"contentVersion"`
	BankVersion     string                  `json:"bankVersion"`
	AppVersion      string                  `json:"appVersion"`
	DBDump          backupFile              `json:"dbDump"`
	DBDumpSHA256    string                  `json:"dbDumpSha256"`
	Tables          map[string]int64        `json:"tables"`
	Originals       []originalEntry         `json:"originals"`
	Derived         []backupFile            `json:"derived,omitempty"`
	Teaching        map[string]any          `json:"teaching,omitempty"`
	Gateway         map[string]any          `json:"gateway,omitempty"`
	Secrets         map[string]any          `json:"secrets,omitempty"`
	Notes           []string                `json:"notes,omitempty"`
	LastVerify      *VerifyReport           `json:"lastVerify,omitempty"`
	Error           string                  `json:"error,omitempty"`
}

type backupFile struct {
	File    string `json:"file,omitempty"`
	RelPath string `json:"rel_path,omitempty"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

type originalEntry struct {
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	RelPath string `json:"rel_path"`
}

type dumpInfo struct {
	SHA256             string
	Size               int64
	Tables             map[string]int64
	ConsistentSnapshot bool
	Method             string
}

// CreateBackup writes one consistency backup batch:
//
//	<BackupDir>/<UTC timestamp>/
//	  db.dump        pg_dump custom-format logical backup
//	  manifest.json  versions, dump sha256, table counts, original inventory
//	  originals/     immutable originals referenced by the blobs table
//	  RESTORE.md     restore instructions
//
// The running PostgreSQL data directory is never copied. The dump is taken
// from a pg_dump snapshot; when snapshot import is available the table counts
// are sampled inside the same exported snapshot.
func (s *Service) CreateBackup(ctx context.Context, actorID string) (Backup, error) {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	if err := s.requireComponentQuiescence(); err != nil {
		return Backup{}, err
	}

	if err := os.MkdirAll(s.cfg.BackupDir(), 0o750); err != nil {
		return Backup{}, fmt.Errorf("创建备份目录失败：%w", err)
	}
	timestamp := time.Now().UTC().Format(backupTimestampFmt)
	dir := filepath.Join(s.cfg.BackupDir(), timestamp)
	if err := os.Mkdir(dir, 0o750); err != nil {
		if os.IsExist(err) {
			return Backup{}, httpx.NewError(http.StatusConflict, "backup_in_progress", "同一秒内已经存在备份批次，请稍后重试。")
		}
		return Backup{}, fmt.Errorf("创建备份批次目录失败：%w", err)
	}
	relPath, err := filepath.Rel(s.cfg.DataDir, dir)
	if err != nil {
		return Backup{}, fmt.Errorf("计算备份相对路径失败：%w", err)
	}
	relPath = filepath.ToSlash(relPath)

	var backupID string
	var createdAt time.Time
	if err := s.db.Pool.QueryRow(ctx, `
		insert into backups (rel_path, status, created_by)
		values ($1, 'creating', nullif($2, '')::uuid)
		returning id::text, created_at`, relPath, actorID).Scan(&backupID, &createdAt); err != nil {
		return Backup{}, fmt.Errorf("写入备份记录失败：%w", err)
	}

	manifest := backupManifest{
		App:           "rka",
		SchemaVersion: backupFormatVersion,
		BackupID:      backupID,
		CreatedAt:     createdAt.UTC(),
		CreatedBy:     actorID,
		AppVersion:    appVersion(),
		Tables:        map[string]int64{},
		Originals:     []originalEntry{},
		Notes:         []string{},
	}
	fail := func(cause error) (Backup, error) {
		manifest.Error = cause.Error()
		payload, _ := json.MarshalIndent(manifest, "", "  ")
		_ = os.WriteFile(filepath.Join(dir, manifestFileName), payload, 0o640)
		updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = s.db.Pool.Exec(updateCtx, `
			update backups set status = 'failed', manifest = $2::jsonb, size_bytes = $3
			where id = $1::uuid`, backupID, string(payload), directorySize(dir))
		return Backup{}, cause
	}

	info, err := s.dumpDatabase(ctx, filepath.Join(dir, dbDumpFileName))
	if err != nil {
		return fail(err)
	}
	manifest.DBDump = backupFile{File: dbDumpFileName, Size: info.Size, SHA256: info.SHA256}
	manifest.DBDumpSHA256 = info.SHA256
	manifest.Tables = info.Tables
	manifest.Notes = append(manifest.Notes, fmt.Sprintf("数据库 dump 方式：%s。", info.Method))

	if version, err := store.BinarySchemaVersion(); err == nil {
		manifest.BinaryVersion = version
	}
	if version, err := s.db.SchemaVersion(ctx); err == nil {
		manifest.DBSchemaVersion = version
	}
	_ = s.db.Pool.QueryRow(ctx, `select content_version, bank_version from platform_state where id = 1`).
		Scan(&manifest.ContentVersion, &manifest.BankVersion)

	originals, err := s.copyOriginals(ctx, dir)
	if err != nil {
		return fail(err)
	}
	manifest.Originals = originals

	derived, err := inventoryFiles(s.cfg.DerivedDir(), 5000)
	if err != nil {
		return fail(fmt.Errorf("读取衍生文件清单失败：%w", err))
	}
	manifest.Derived = derived

	components, err := s.copyComponents(ctx, dir)
	if err != nil {
		return fail(err)
	}
	manifest.Components = components
	manifest.Teaching = map[string]any{"status": "snapshotted", "fileCount": len(components["teaching"])}
	manifest.Gateway = map[string]any{"status": "snapshotted", "fileCount": len(components["gateway"])}
	secrets, err := s.copySecrets(dir)
	if err != nil {
		return fail(err)
	}
	manifest.Secrets = secrets
	manifest.Notes = append(manifest.Notes, "应用密钥 secret-key 随批次备份（敏感数据，请按密钥保管）；bootstrap-token 不进入备份。")
	manifest.Notes = append(manifest.Notes, "derived/ 与 cache/ 可重建，只记录清单，不复制内容。")

	readme, err := s.renderRestoreReadme(manifest, relPath)
	if err != nil {
		return fail(fmt.Errorf("生成恢复说明失败：%w", err))
	}
	if err := os.WriteFile(filepath.Join(dir, restoreReadmeName), []byte(readme), 0o640); err != nil {
		return fail(fmt.Errorf("写入恢复说明失败：%w", err))
	}

	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fail(fmt.Errorf("序列化 manifest 失败：%w", err))
	}
	manifestPath := filepath.Join(dir, manifestFileName)
	if err := os.WriteFile(manifestPath, payload, 0o640); err != nil {
		return fail(fmt.Errorf("写入 manifest 失败：%w", err))
	}
	manifestSHA := sha256Hex(payload)
	totalSize := directorySize(dir)
	if _, err := s.db.Pool.Exec(ctx, `
		update backups set size_bytes = $2, sha256 = $3, manifest = $4::jsonb, status = 'complete'
		where id = $1::uuid`, backupID, totalSize, manifestSHA, string(payload)); err != nil {
		return fail(fmt.Errorf("更新备份记录失败：%w", err))
	}

	_ = manifestSHA
	return Backup{
		ID:        backupID,
		RelPath:   relPath,
		Path:      dir,
		Size:      totalSize,
		SHA256:    manifestSHA,
		Status:    "complete",
		CreatedAt: createdAt.UTC(),
	}, nil
}

// dumping helpers -----------------------------------------------------------

func (s *Service) dumpDatabase(ctx context.Context, outPath string) (dumpInfo, error) {
	tool, err := pgToolPath("RKA_PG_DUMP", "pg_dump")
	if err != nil {
		return dumpInfo{}, err
	}
	conn, err := parseConnInfo(s.cfg.DatabaseURL)
	if err != nil {
		return dumpInfo{}, err
	}

	// Preferred: export a repeatable-read snapshot, let pg_dump import it, and
	// count rows inside the same transaction so manifest counts exactly match
	// the dump.
	info, snapshotErr := s.dumpWithSnapshot(ctx, tool, conn, outPath)
	if snapshotErr == nil {
		return info, nil
	}
	s.log.Warn("pg_dump snapshot mode failed; retrying with the default pg_dump snapshot", "error", snapshotErr)

	info, err = s.dumpPlain(ctx, tool, conn, outPath)
	if err != nil {
		return dumpInfo{}, fmt.Errorf("pg_dump 失败：%v（快照模式错误：%v）", err, snapshotErr)
	}
	info.Method = "pg_dump 默认一致性快照（行数在 dump 完成后采样）"
	info.ConsistentSnapshot = true
	return info, nil
}

func (s *Service) dumpWithSnapshot(ctx context.Context, tool string, conn connInfo, outPath string) (dumpInfo, error) {
	tx, err := s.db.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return dumpInfo{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var snapshot string
	if err := tx.QueryRow(ctx, `select pg_export_snapshot()`).Scan(&snapshot); err != nil {
		return dumpInfo{}, err
	}
	args := append(conn.pgDumpArgs(), "--snapshot="+snapshot)
	if err := runCommandToFile(ctx, tool, args, conn.env(), outPath); err != nil {
		return dumpInfo{}, err
	}
	tables, err := tableCounts(ctx, tx)
	if err != nil {
		return dumpInfo{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return dumpInfo{}, err
	}
	return dumpInfo{
		SHA256:             fileSHA256(outPath),
		Size:               fileSize(outPath),
		Tables:             tables,
		ConsistentSnapshot: true,
		Method:             "pg_dump --snapshot（pg_export_snapshot，同一事务内采样行数）",
	}, nil
}

func (s *Service) dumpPlain(ctx context.Context, tool string, conn connInfo, outPath string) (dumpInfo, error) {
	if err := runCommandToFile(ctx, tool, conn.pgDumpArgs(), conn.env(), outPath); err != nil {
		return dumpInfo{}, err
	}
	tables, err := tableCounts(ctx, s.db.Pool)
	if err != nil {
		return dumpInfo{}, fmt.Errorf("统计表行数失败：%w", err)
	}
	return dumpInfo{
		SHA256:             fileSHA256(outPath),
		Size:               fileSize(outPath),
		Tables:             tables,
		ConsistentSnapshot: true,
	}, nil
}

// querier covers pgxpool.Pool and pgx.Tx so tableCounts works in both modes.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func tableCounts(ctx context.Context, q querier) (map[string]int64, error) {
	counts := make(map[string]int64, len(backupTables))
	for _, table := range backupTables {
		var count int64
		if err := q.QueryRow(ctx, `select count(*) from "`+table+`"`).Scan(&count); err != nil {
			return nil, fmt.Errorf("统计表 %s 行数失败：%w", table, err)
		}
		counts[table] = count
	}
	return counts, nil
}

type connInfo struct {
	User     string
	Password string
	Host     string
	Port     string
	Database string
	SSLMode  string
}

func parseConnInfo(dsn string) (connInfo, error) {
	parsed, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil {
		return connInfo{}, fmt.Errorf("解析 RKA_DATABASE_URL 失败：%w", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return connInfo{}, fmt.Errorf("RKA_DATABASE_URL 必须是 postgres:// 连接串")
	}
	info := connInfo{
		Host:    parsed.Hostname(),
		Port:    parsed.Port(),
		SSLMode: parsed.Query().Get("sslmode"),
	}
	if parsed.User != nil {
		info.User = parsed.User.Username()
		info.Password, _ = parsed.User.Password()
	}
	info.Database = strings.TrimPrefix(parsed.Path, "/")
	if info.Database == "" {
		info.Database = "postgres"
	}
	return info, nil
}

func (c connInfo) pgDumpArgs() []string {
	args := []string{"--format=custom", "--no-owner", "--no-privileges", "--no-password", "--lock-wait-timeout=15s"}
	if c.Host != "" {
		args = append(args, "--host="+c.Host)
	}
	if c.Port != "" {
		args = append(args, "--port="+c.Port)
	}
	if c.User != "" {
		args = append(args, "--username="+c.User)
	}
	if c.Database != "" {
		args = append(args, "--dbname="+c.Database)
	}
	return args
}

func (c connInfo) pgRestoreArgs(targetDSN string) []string {
	args := []string{"--no-owner", "--no-privileges", "--single-transaction", "--exit-on-error"}
	if targetDSN != "" {
		args = append(args, "--dbname="+targetDSN)
	}
	return args
}

func (c connInfo) env() []string {
	env := append([]string{}, os.Environ()...)
	if c.Password != "" {
		env = append(env, "PGPASSWORD="+c.Password)
	}
	if c.SSLMode != "" {
		env = append(env, "PGSSLMODE="+c.SSLMode)
	}
	env = append(env, "PGCONNECT_TIMEOUT=15")
	return env
}

// dsnFor returns a connection string for another database on the same server,
// never including the password in logs or manifests.
func (c connInfo) dsnFor(database string) string {
	user := ""
	if c.User != "" {
		user = url.UserPassword(c.User, c.Password).String()
	}
	host := c.Host
	if c.Port != "" {
		host = host + ":" + c.Port
	}
	query := ""
	if c.SSLMode != "" {
		query = "?sslmode=" + url.QueryEscape(c.SSLMode)
	}
	return "postgres://" + user + "@" + host + "/" + database + query
}

// dsnForCommand returns a connection string that is safe to place in a
// command-line argv: it never contains the password. The password is passed to
// pg_restore through PGPASSWORD in the child process environment instead, so it
// cannot be read by other local users through ps or /proc.
func (c connInfo) dsnForCommand(database string) string {
	user := ""
	if c.User != "" {
		user = url.User(c.User).String()
	}
	host := c.Host
	if c.Port != "" {
		host = host + ":" + c.Port
	}
	query := ""
	if c.SSLMode != "" {
		query = "?sslmode=" + url.QueryEscape(c.SSLMode)
	}
	return "postgres://" + user + "@" + host + "/" + database + query
}

// dsnHint returns a password-free hint for humans and RESTORE.md.
func (c connInfo) dsnHint(database string) string {
	user := ""
	if c.User != "" {
		user = c.User
	}
	host := c.Host
	if c.Port != "" {
		host = host + ":" + c.Port
	}
	query := ""
	if c.SSLMode != "" {
		query = "?sslmode=" + url.QueryEscape(c.SSLMode)
	}
	return "postgres://" + user + "@" + host + "/" + database + query
}

func runCommandToFile(ctx context.Context, tool string, args, env []string, outPath string) error {
	file, err := os.OpenFile(outPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("创建 dump 文件失败：%w", err)
	}
	defer file.Close()
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Env = env
	cmd.Stdout = file
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s 执行失败：%v：%s", filepath.Base(tool), err, truncate(stderr.String(), 800))
	}
	return nil
}

// pgToolPath resolves pg_dump/pg_restore from an explicit environment override,
// PATH, or common PostgreSQL installation directories. It never guesses that a
// missing tool succeeded.
func pgToolPath(envKey, name string) (string, error) {
	if candidate := strings.TrimSpace(os.Getenv(envKey)); candidate != "" {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved, nil
		}
		return "", fmt.Errorf("%s 指向的 %s 不存在：%s", envKey, name, candidate)
	}
	if resolved, err := exec.LookPath(name); err == nil {
		return resolved, nil
	}
	patterns := []string{
		"/usr/lib/postgresql/*/bin/" + name,
		"/usr/local/pgsql/bin/" + name,
		"/opt/homebrew/opt/postgresql*/bin/" + name,
	}
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		sort.Strings(matches)
		for i := len(matches) - 1; i >= 0; i-- {
			if info, err := os.Stat(matches[i]); err == nil && !info.IsDir() {
				return matches[i], nil
			}
		}
	}
	return "", fmt.Errorf("未找到 %s：请在运行环境安装 PostgreSQL 客户端（pg_dump/pg_restore），或通过 %s 指定路径。", name, envKey)
}

// originals ----------------------------------------------------------------

// copyOriginals copies the immutable originals referenced by the blobs table.
// When the blobs table is empty (content not imported yet) it falls back to a
// full inventory of the originals directory so the batch still records what
// exists on disk.
func (s *Service) copyOriginals(ctx context.Context, dir string) ([]originalEntry, error) {
	rows, err := s.db.Pool.Query(ctx, `select sha256, size, rel_path from blobs order by rel_path`)
	if err != nil {
		return nil, fmt.Errorf("读取 blobs 清单失败：%w", err)
	}
	type blobRow struct {
		SHA256  string
		Size    int64
		RelPath string
	}
	var blobs []blobRow
	for rows.Next() {
		var row blobRow
		if err := rows.Scan(&row.SHA256, &row.Size, &row.RelPath); err != nil {
			rows.Close()
			return nil, fmt.Errorf("读取 blobs 清单失败：%w", err)
		}
		blobs = append(blobs, row)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取 blobs 清单失败：%w", err)
	}

	targetDir := filepath.Join(dir, originalsDirName)
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		return nil, fmt.Errorf("创建原件备份目录失败：%w", err)
	}

	if len(blobs) == 0 {
		return s.copyOriginalsFromDisk(targetDir)
	}

	entries := make([]originalEntry, 0, len(blobs))
	for _, blob := range blobs {
		src, err := resolveOriginalPath(s.cfg, blob.RelPath)
		if err != nil {
			return nil, fmt.Errorf("原件路径不安全：%s", blob.RelPath)
		}
		if _, err := os.Stat(src); err != nil {
			return nil, fmt.Errorf("blobs 记录的原件缺失：%s（%v）", blob.RelPath, err)
		}
		rel := sanitizeRelPath(blob.RelPath)
		dst := filepath.Join(targetDir, rel)
		size, sha, err := copyFileVerified(src, dst, blob.SHA256, blob.Size)
		if err != nil {
			return nil, fmt.Errorf("复制原件 %s 失败：%w", blob.RelPath, err)
		}
		entries = append(entries, originalEntry{SHA256: sha, Size: size, RelPath: rel})
	}
	return entries, nil
}

func (s *Service) copyOriginalsFromDisk(targetDir string) ([]originalEntry, error) {
	root := s.cfg.OriginalsDir()
	var entries []originalEntry
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return entries, nil
		}
		return nil, fmt.Errorf("读取原件目录失败：%w", err)
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		dst := filepath.Join(targetDir, rel)
		size, sha, err := copyFileVerified(path, dst, "", -1)
		if err != nil {
			return err
		}
		entries = append(entries, originalEntry{SHA256: sha, Size: size, RelPath: rel})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("复制原件失败：%w", err)
	}
	return entries, nil
}

// resolveOriginalPath finds a blob on disk. blobs.rel_path may be relative to
// the data root or to data/originals depending on the content importer.
func resolveOriginalPath(cfg interface {
	DataPath(parts ...string) string
	OriginalsDir() string
}, relPath string) (string, error) {
	clean := filepath.Clean(strings.TrimPrefix(relPath, "/"))
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", errors.New("path escapes originals root")
	}
	candidates := []string{
		cfg.DataPath(clean),
		filepath.Join(cfg.OriginalsDir(), clean),
		filepath.Join(cfg.OriginalsDir(), filepath.Base(clean)),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return candidates[0], nil
}

func sanitizeRelPath(relPath string) string {
	clean := filepath.Clean(strings.TrimPrefix(relPath, "/"))
	clean = strings.TrimPrefix(clean, "originals"+string(os.PathSeparator))
	if clean == "." || clean == "" || strings.HasPrefix(clean, "..") {
		return filepath.Base(relPath)
	}
	return clean
}

func copyFileVerified(src, dst, wantSHA string, wantSize int64) (int64, string, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return 0, "", err
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, "", err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(out, hash), in)
	closeErr := out.Close()
	if copyErr != nil {
		return 0, "", copyErr
	}
	if closeErr != nil {
		return 0, "", closeErr
	}
	sha := hex.EncodeToString(hash.Sum(nil))
	if wantSHA != "" && sha != wantSHA {
		return 0, "", fmt.Errorf("sha256 不一致：期望 %s，实际 %s", wantSHA, sha)
	}
	if wantSize >= 0 && written != wantSize {
		return 0, "", fmt.Errorf("文件大小不一致：期望 %d，实际 %d", wantSize, written)
	}
	return written, sha, nil
}

// inventories --------------------------------------------------------------

func inventoryFiles(root string, limit int) ([]backupFile, error) {
	var files []backupFile
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return files, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s 不是目录", root)
	}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if len(files) >= limit {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		files = append(files, backupFile{RelPath: filepath.ToSlash(rel), Size: info.Size()})
		return nil
	})
	return files, err
}

func (s *Service) teachingInventory() map[string]any {
	configured := strings.TrimSpace(s.cfg.Teaching.DeepTutorURL) != ""
	inventory := map[string]any{"configured": configured}
	if !configured {
		inventory["status"] = "not_configured"
		inventory["reason"] = "未配置 DeepTutor；无教学持久目录需要快照。"
		return inventory
	}
	files, err := inventoryFiles(s.cfg.TeachingDir(), 2000)
	if err != nil {
		inventory["status"] = "failed"
		inventory["reason"] = "读取教学目录清单失败：" + err.Error()
		return inventory
	}
	inventory["status"] = "inventory_only"
	inventory["files"] = files
	inventory["reason"] = "DeepTutor 一致性快照应由其自身维护窗口导出；本批次只记录持久目录清单，不复制运行中的教学数据。"
	return inventory
}

func (s *Service) gatewayInventory() map[string]any {
	files, err := inventoryFiles(s.cfg.GatewayDir(), 1000)
	if err != nil {
		return map[string]any{"status": "failed", "reason": err.Error()}
	}
	if len(files) == 0 {
		return map[string]any{"status": "empty"}
	}
	return map[string]any{"status": "configured", "files": files}
}

// copySecrets includes the application secret key in the batch so a restored
// data root can decrypt model credentials and teaching identities. The
// bootstrap token is never included.
func (s *Service) copySecrets(dir string) (map[string]any, error) {
	info := map[string]any{
		"included":  false,
		"excluded":  []string{"bootstrap-token"},
		"sensitive": true,
		"reason":    "未找到 data/secrets/secret-key（密钥可能通过 RKA_SECRET_KEY 环境变量提供）；恢复后需单独提供密钥，否则模型凭据密文和教学身份无法解密。",
	}
	source := filepath.Join(s.cfg.SecretsDir(), "secret-key")
	raw, err := os.ReadFile(source)
	if err != nil {
		if os.IsNotExist(err) {
			return info, nil
		}
		return nil, fmt.Errorf("读取应用密钥失败：%w", err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return nil, fmt.Errorf("应用密钥文件为空：%s", source)
	}
	if err := os.MkdirAll(filepath.Join(dir, "secrets"), 0o700); err != nil {
		return nil, fmt.Errorf("创建密钥备份目录失败：%w", err)
	}
	target := filepath.Join(dir, filepath.FromSlash(secretKeyRelPath))
	if err := os.WriteFile(target, raw, 0o600); err != nil {
		return nil, fmt.Errorf("复制应用密钥失败：%w", err)
	}
	if err := os.Chmod(target, 0o600); err != nil {
		return nil, fmt.Errorf("设置应用密钥权限失败：%w", err)
	}
	info = map[string]any{
		"included":  true,
		"file":      secretKeyRelPath,
		"sha256":    sha256Hex(raw),
		"size":      int64(len(raw)),
		"sensitive": true,
		"note":      "备份批次包含应用密钥，属于敏感数据：请按密钥保管，不要提交到公开仓库或普通共享。",
		"excluded":  []string{"bootstrap-token"},
	}
	return info, nil
}

func inspectSecretKey(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	decoded, err := hex.DecodeString(trimmed)
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("不是 32 字节 hex 密钥（%d 个字符）", len(trimmed))
	}
	return nil
}

func verifySecretKeyFile(backupDir string, info map[string]any) error {
	relFile := secretKeyRelPath
	if value, ok := info["file"].(string); ok && value != "" {
		relFile = value
	}
	path, err := safeJoin(backupDir, relFile)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if want, ok := info["sha256"].(string); ok && want != "" && sha256Hex(raw) != want {
		return fmt.Errorf("sha256 不一致")
	}
	return inspectSecretKey(raw)
}

// restoreSecrets copies the backed-up application key into the new data root
// with owner-only permissions.
func restoreSecrets(backupDir, target string, manifest backupManifest) (bool, error) {
	included, _ := manifest.Secrets["included"].(bool)
	if !included {
		return false, nil
	}
	relFile := secretKeyRelPath
	if value, ok := manifest.Secrets["file"].(string); ok && value != "" {
		relFile = value
	}
	src, err := safeJoin(backupDir, relFile)
	if err != nil {
		return false, fmt.Errorf("应用密钥路径不安全：%w", err)
	}
	wantSHA, _ := manifest.Secrets["sha256"].(string)
	wantSize := int64(-1)
	if value, ok := manifest.Secrets["size"].(float64); ok {
		wantSize = int64(value)
	}
	dst, err := safeJoin(target, relFile)
	if err != nil {
		return false, fmt.Errorf("应用密钥恢复路径不安全：%w", err)
	}
	if _, _, err := copyFileVerified(src, dst, wantSHA, wantSize); err != nil {
		return false, fmt.Errorf("复制应用密钥到恢复目录失败：%w", err)
	}
	if err := os.Chmod(dst, 0o600); err != nil {
		return false, fmt.Errorf("设置恢复目录应用密钥权限失败：%w", err)
	}
	raw, err := os.ReadFile(dst)
	if err != nil {
		return false, fmt.Errorf("读取恢复目录应用密钥失败：%w", err)
	}
	if err := inspectSecretKey(raw); err != nil {
		return false, fmt.Errorf("恢复目录中的应用密钥无效：%w", err)
	}
	return true, nil
}

// Verify -------------------------------------------------------------------

// VerifyBackup performs a real restore rehearsal: manifest hashes are checked,
// original files are hashed, and db.dump is restored into a temporary database
// on the same PostgreSQL server where row counts are compared with the
// manifest. The temporary database is always dropped.
func (s *Service) VerifyBackup(ctx context.Context, idOrPath string) (VerifyReport, error) {
	report := VerifyReport{ID: strings.TrimSpace(idOrPath), Checks: []string{}, Errors: []string{}, Warnings: []string{}, VerifiedAt: time.Now().UTC()}
	dir, rowID, err := s.resolveBackupDir(ctx, idOrPath)
	if err != nil {
		return report, err
	}
	if rowID != "" {
		report.ID = rowID
	}
	manifest, err := readManifest(dir)
	if err != nil {
		report.Errors = append(report.Errors, "读取 manifest.json 失败："+err.Error())
		return report, nil
	}
	if manifest.App != "rka" {
		report.Errors = append(report.Errors, "manifest.app 不是 rka，无法识别该备份。")
	}
	if manifest.SchemaVersion != backupFormatVersion {
		report.Errors = append(report.Errors, fmt.Sprintf("不支持的备份格式版本 %d（期望 %d）。", manifest.SchemaVersion, backupFormatVersion))
	}
	report.Checks = append(report.Checks, "manifest.json 可读取")
	if err := componentFiles(dir, manifest, nil); err != nil {
		report.Errors = append(report.Errors, "教学/网关快照校验失败："+err.Error())
	} else {
		report.Checks = append(report.Checks, "教学/网关持久文件哈希校验通过")
	}

	dumpPath := filepath.Join(dir, dbDumpFileName)
	if manifest.DBDump.File != "" {
		dumpPath = filepath.Join(dir, filepath.Base(manifest.DBDump.File))
	}
	if err := verifyFileHash(dumpPath, manifest.DBDump.SHA256, manifest.DBDump.Size); err != nil {
		report.Errors = append(report.Errors, "db.dump 校验失败："+err.Error())
	} else {
		report.Checks = append(report.Checks, fmt.Sprintf("db.dump sha256 一致（%d 字节）", manifest.DBDump.Size))
	}

	originalChecked := 0
	originalFailed := 0
	for _, original := range manifest.Originals {
		if originalChecked >= 5000 {
			break
		}
		path, err := safeJoin(filepath.Join(dir, originalsDirName), original.RelPath)
		if err != nil {
			originalFailed++
			if originalFailed <= 20 {
				report.Errors = append(report.Errors, "原件路径不安全："+original.RelPath+"："+err.Error())
			}
			continue
		}
		if err := verifyFileHash(path, original.SHA256, original.Size); err != nil {
			originalFailed++
			if originalFailed <= 20 {
				report.Errors = append(report.Errors, "原件校验失败："+original.RelPath+"："+err.Error())
			}
			continue
		}
		originalChecked++
	}
	if originalFailed == 0 {
		report.Checks = append(report.Checks, fmt.Sprintf("原件 sha256 一致（%d 个文件）", originalChecked))
		if err := verifyOriginalsInTempDir(dir, manifest.Originals); err != nil {
			report.Errors = append(report.Errors, "原件隔离目录恢复校验失败："+err.Error())
		} else {
			report.Checks = append(report.Checks, fmt.Sprintf("原件已复制到隔离临时目录并校验（%d 个文件）", len(manifest.Originals)))
		}
	}
	if included, _ := manifest.Secrets["included"].(bool); included {
		if err := verifySecretKeyFile(dir, manifest.Secrets); err != nil {
			report.Errors = append(report.Errors, "应用密钥校验失败："+err.Error())
		} else {
			report.Checks = append(report.Checks, secretKeyRelPath+" 存在且为 32 字节 hex（sha256 一致）")
		}
	} else {
		report.Checks = append(report.Checks, secretKeyRelPath+" 未包含（warned）：旧格式备份或密钥通过环境变量提供，恢复后需单独提供 RKA_SECRET_KEY")
		report.Warnings = append(report.Warnings, "备份批次未包含 "+secretKeyRelPath+"；恢复后需单独提供 RKA_SECRET_KEY，否则模型凭据与教学身份无法解密。")
	}

	// Restore rehearsal into a temporary database on the same server.
	if err := s.restoreRehearsal(ctx, manifest, dumpPath, &report); err != nil {
		report.Errors = append(report.Errors, err.Error())
	}

	report.OK = len(report.Errors) == 0
	if rowID != "" {
		status := "complete"
		if report.OK {
			status = "verified"
		}
		payload, _ := json.Marshal(report)
		updateCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = s.db.Pool.Exec(updateCtx, `
			update backups set status = $2,
			       manifest = jsonb_set(manifest, '{lastVerify}', $3::jsonb, true)
			where id = $1::uuid`, rowID, status, string(payload))
	}
	return report, nil
}

// verifyOriginalsInTempDir copies the original files into a throwaway
// directory and re-checks their hashes, proving the backup files can actually
// be restored, not merely hashed in place.
func verifyOriginalsInTempDir(backupDir string, originals []originalEntry) error {
	tempDir, err := os.MkdirTemp("", "rka-verify-originals-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	for i, original := range originals {
		if i >= 5000 {
			break
		}
		src, err := safeJoin(filepath.Join(backupDir, originalsDirName), original.RelPath)
		if err != nil {
			return fmt.Errorf("%s：%w", original.RelPath, err)
		}
		dst, err := safeJoin(tempDir, original.RelPath)
		if err != nil {
			return fmt.Errorf("%s：%w", original.RelPath, err)
		}
		if _, _, err := copyFileVerified(src, dst, original.SHA256, original.Size); err != nil {
			return fmt.Errorf("%s：%w", original.RelPath, err)
		}
	}
	return nil
}

func (s *Service) restoreRehearsal(ctx context.Context, manifest backupManifest, dumpPath string, report *VerifyReport) error {
	tool, err := pgToolPath("RKA_PG_RESTORE", "pg_restore")
	if err != nil {
		return fmt.Errorf("无法执行隔离恢复校验：%v", err)
	}
	conn, err := parseConnInfo(s.cfg.DatabaseURL)
	if err != nil {
		return err
	}
	tempDB := "rka_verify_" + randomHex(8)
	if _, err := s.db.Pool.Exec(ctx, `create database "`+tempDB+`"`); err != nil {
		return fmt.Errorf("创建临时数据库 %s 失败：%v", tempDB, err)
	}
	dropped := false
	drop := func() {
		if dropped {
			return
		}
		dropped = true
		dropCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if _, err := s.db.Pool.Exec(dropCtx, `drop database if exists "`+tempDB+`" with (force)`); err != nil {
			s.log.Warn("drop verification database failed", "database", tempDB, "error", err)
		}
	}
	defer drop()

	// pgx connects with the in-memory DSN (password allowed); the command line
	// only ever sees a password-free DSN and receives the secret via PGPASSWORD.
	targetDSN := conn.dsnFor(tempDB)
	commandDSN := conn.dsnForCommand(tempDB)
	cmd := exec.CommandContext(ctx, tool, conn.pgRestoreArgs(commandDSN)...)
	in, err := os.Open(dumpPath)
	if err != nil {
		return fmt.Errorf("打开 db.dump 失败：%v", err)
	}
	defer in.Close()
	cmd.Stdin = in
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = conn.env()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore 恢复到临时数据库 %s 失败：%v：%s", tempDB, err, truncate(stderr.String(), 800))
	}
	report.Checks = append(report.Checks, "pg_restore 恢复临时数据库 "+tempDB+" 成功")

	tempConn, err := pgx.Connect(ctx, targetDSN)
	if err != nil {
		return fmt.Errorf("连接临时数据库失败：%v", err)
	}
	defer tempConn.Close(context.WithoutCancel(ctx))

	var restoredSchema int64
	_ = tempConn.QueryRow(ctx, `select coalesce(max(version), 0) from schema_migrations`).Scan(&restoredSchema)
	if manifest.DBSchemaVersion != 0 && restoredSchema != manifest.DBSchemaVersion {
		report.Errors = append(report.Errors, fmt.Sprintf("schema 版本不一致：备份 manifest=%d，恢复库=%d", manifest.DBSchemaVersion, restoredSchema))
	} else {
		report.Checks = append(report.Checks, fmt.Sprintf("schema 版本一致（v%d）", restoredSchema))
	}

	mismatches := 0
	for _, table := range backupTables {
		want, recorded := manifest.Tables[table]
		if !recorded {
			continue
		}
		var got int64
		if err := tempConn.QueryRow(ctx, `select count(*) from "`+table+`"`).Scan(&got); err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("读取恢复库表 %s 行数失败：%v", table, err))
			mismatches++
			continue
		}
		if got != want {
			mismatches++
			if mismatches <= 20 {
				report.Errors = append(report.Errors, fmt.Sprintf("表 %s 行数不一致：备份=%d，恢复=%d", table, want, got))
			}
		}
	}
	if mismatches == 0 {
		report.Checks = append(report.Checks, fmt.Sprintf("代表性表行数与 manifest 一致（%d 张表）", len(manifest.Tables)))
	}
	return nil
}

// Restore ------------------------------------------------------------------

// RestoreResult describes a completed restore-to-new-directory operation.
type RestoreResult struct {
	TargetDir       string   `json:"targetDir"`
	DatabaseDump    string   `json:"databaseDump"`
	Originals       int      `json:"originals"`
	SecretsIncluded bool     `json:"secretsIncluded"`
	Notes           []string `json:"notes"`
}

// RestoreBackup restores a backup into a new data root. It never modifies the
// current data root and never touches the live database.
func (s *Service) RestoreBackup(ctx context.Context, id, targetDir string) error {
	_, err := s.Restore(ctx, id, targetDir)
	return err
}

// Restore copies the dump and originals into targetDir and writes RESTORE.md.
// The operator then creates an empty database, runs pg_restore and starts the
// application with RKA_DATA_DIR=targetDir.
func (s *Service) Restore(ctx context.Context, id, targetDir string) (RestoreResult, error) {
	result := RestoreResult{Notes: []string{}}
	dir, _, err := s.resolveBackupDir(ctx, id)
	if err != nil {
		return result, err
	}
	manifest, err := readManifest(dir)
	if err != nil {
		return result, httpx.NewError(http.StatusUnprocessableEntity, "backup_manifest_invalid", "读取备份 manifest 失败："+err.Error())
	}
	if err := componentFiles(dir, manifest, nil); err != nil {
		return result, err
	}

	dumpPath := filepath.Join(dir, dbDumpFileName)
	if manifest.DBDump.File != "" {
		dumpPath = filepath.Join(dir, filepath.Base(manifest.DBDump.File))
	}
	if err := verifyFileHash(dumpPath, manifest.DBDump.SHA256, manifest.DBDump.Size); err != nil {
		return result, httpx.NewError(http.StatusUnprocessableEntity, "backup_corrupt", "备份 db.dump 校验失败，拒绝恢复："+err.Error())
	}
	for _, original := range manifest.Originals {
		path, err := safeJoin(filepath.Join(dir, originalsDirName), original.RelPath)
		if err != nil {
			return result, httpx.NewError(http.StatusUnprocessableEntity, "backup_corrupt", "备份原件路径不安全，拒绝恢复："+original.RelPath+"："+err.Error())
		}
		if err := verifyFileHash(path, original.SHA256, original.Size); err != nil {
			return result, httpx.NewError(http.StatusUnprocessableEntity, "backup_corrupt", "备份原件校验失败，拒绝恢复："+original.RelPath+"："+err.Error())
		}
	}

	target, err := validateRestoreTarget(s.cfg.DataDir, dir, targetDir)
	if err != nil {
		return result, err
	}
	if err := createDataRoot(target); err != nil {
		return result, fmt.Errorf("创建恢复目录结构失败：%w", err)
	}

	originals := 0
	for _, original := range manifest.Originals {
		src, err := safeJoin(filepath.Join(dir, originalsDirName), original.RelPath)
		if err != nil {
			return result, httpx.NewError(http.StatusUnprocessableEntity, "backup_corrupt", "备份原件路径不安全，拒绝恢复："+original.RelPath+"："+err.Error())
		}
		dst, err := safeJoin(filepath.Join(target, originalsDirName), original.RelPath)
		if err != nil {
			return result, fmt.Errorf("恢复目录原件路径不安全：%w", err)
		}
		if _, _, err := copyFileVerified(src, dst, original.SHA256, original.Size); err != nil {
			return result, fmt.Errorf("复制原件 %s 失败：%w", original.RelPath, err)
		}
		originals++
	}

	if err := restoreComponents(dir, target, manifest); err != nil {
		return result, err
	}

	dumpDst := filepath.Join(target, dbDumpFileName)
	if _, _, err := copyFileVerified(dumpPath, dumpDst, manifest.DBDump.SHA256, manifest.DBDump.Size); err != nil {
		return result, fmt.Errorf("复制 db.dump 失败：%w", err)
	}
	manifestPayload, err := os.ReadFile(filepath.Join(dir, manifestFileName))
	if err != nil {
		return result, fmt.Errorf("读取备份 manifest.json 失败：%w", err)
	}
	if err := os.WriteFile(filepath.Join(target, manifestFileName), manifestPayload, 0o640); err != nil {
		return result, fmt.Errorf("写入 manifest.json 失败：%w", err)
	}
	relToData, _ := filepath.Rel(s.cfg.DataDir, dir)
	readme, err := s.renderRestoreReadme(manifest, filepath.ToSlash(relToData))
	if err != nil {
		return result, err
	}
	if err := os.WriteFile(filepath.Join(target, restoreReadmeName), []byte(readme), 0o640); err != nil {
		return result, fmt.Errorf("写入 RESTORE.md 失败：%w", err)
	}
	secretsIncluded, err := restoreSecrets(dir, target, manifest)
	if err != nil {
		return result, err
	}
	result.SecretsIncluded = secretsIncluded

	// Best effort: carry the current binary into the restored root so the
	// documented `./data/restore-drill/rka serve` drill works. During tests the
	// executable is the test binary, so only a real `rka` binary is copied.
	if executable, err := os.Executable(); err == nil && filepath.Base(executable) == "rka" {
		if _, _, err := copyFileVerified(executable, filepath.Join(target, "rka"), "", -1); err == nil {
			_ = os.Chmod(filepath.Join(target, "rka"), 0o755)
		}
	}

	result.TargetDir = target
	result.DatabaseDump = dumpDst
	result.Originals = originals
	result.Notes = append(result.Notes,
		"该目录是全新的数据根，未修改现有 RKA_DATA_DIR。",
		"必须先在备份数据库服务器上创建空数据库，并用 pg_restore 把 db.dump 导入该库。",
		"导入完成后，用 RKA_DATA_DIR="+target+" RKA_DATABASE_URL=<新库> 启动应用；不要指向现有数据根。",
	)
	if secretsIncluded {
		result.Notes = append(result.Notes,
			"备份包含应用密钥，已复制到 "+filepath.Join(target, filepath.FromSlash(secretKeyRelPath))+"；启动时用 RKA_SECRET_KEY_FILE 指向它，并按密钥保管恢复目录。")
	} else {
		result.Notes = append(result.Notes, "备份未包含应用密钥；启动时需单独提供 RKA_SECRET_KEY。")
	}
	nextSteps := "1) 创建空数据库；2) pg_restore --no-owner --no-privileges --single-transaction --dbname <新库> " + dumpDst +
		"；3) RKA_DATA_DIR=" + target + " RKA_DATABASE_URL=<新库> rka serve。不要用恢复目录覆盖现有数据根。"
	s.log.Info("backup restored", "target_dir", target, "database_dump", dumpDst, "secrets_included", secretsIncluded, "next_steps", nextSteps)
	if secretsIncluded {
		s.log.Info("restored application secret key", "path", filepath.Join(target, filepath.FromSlash(secretKeyRelPath)),
			"note", "备份包含应用密钥，属于敏感数据；请按密钥保管该目录")
	}
	return result, nil
}

func validateRestoreTarget(dataDir, backupDir, targetDir string) (string, error) {
	value := strings.TrimSpace(targetDir)
	if value == "" {
		return "", httpx.NewError(http.StatusBadRequest, "bad_request", "targetDir 不能为空。")
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", httpx.NewError(http.StatusBadRequest, "bad_request", "targetDir 路径无效："+err.Error())
	}
	abs = filepath.Clean(abs)
	dataAbs := filepath.Clean(dataDir)
	backupAbs := filepath.Clean(backupDir)
	if abs == dataAbs {
		return "", httpx.NewError(http.StatusConflict, "target_is_data_root", "不能把备份恢复到当前数据根目录，请指定一个新目录。")
	}
	if isWithin(abs, dataAbs) {
		return "", httpx.NewError(http.StatusConflict, "target_contains_data_root", "目标目录包含当前数据根目录，拒绝覆盖。")
	}
	if abs == backupAbs || isWithin(abs, backupAbs) || isWithin(backupAbs, abs) {
		return "", httpx.NewError(http.StatusConflict, "target_is_backup", "不能把备份恢复到备份批次目录内，也不能覆盖备份目录。")
	}
	if info, err := os.Stat(abs); err == nil {
		if !info.IsDir() {
			return "", httpx.NewError(http.StatusConflict, "target_not_empty", "目标路径已存在且不是目录。")
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			return "", httpx.NewError(http.StatusBadRequest, "bad_request", "读取目标目录失败："+err.Error())
		}
		if len(entries) > 0 {
			return "", httpx.NewError(http.StatusConflict, "target_not_empty", "目标目录已存在且不为空，请指定新的空目录。")
		}
	}
	return abs, nil
}

// isWithin reports whether child is inside parent (or equal).
func isWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

// safeJoin joins a manifest-provided relative path under root and rejects
// absolute paths and traversal outside root. Verify and restore both use it so
// a tampered manifest cannot read or write outside the batch directory.
func safeJoin(root, rel string) (string, error) {
	value := strings.TrimSpace(rel)
	if value == "" {
		return "", fmt.Errorf("路径为空")
	}
	cleaned := filepath.Clean(filepath.FromSlash(value))
	if filepath.IsAbs(cleaned) || cleaned == "." || cleaned == ".." ||
		strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("路径越出根目录：%q", rel)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(absRoot, cleaned)
	if !isWithin(absRoot, joined) || filepath.Clean(joined) == filepath.Clean(absRoot) {
		return "", fmt.Errorf("路径越出根目录：%q", rel)
	}
	return joined, nil
}

func createDataRoot(root string) error {
	for _, name := range []string{
		"", "postgres", originalsDirName, "derived", "exports", "teaching",
		"cache", "gateway", "logs", "secrets", "backups",
	} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o750); err != nil {
			return err
		}
	}
	return nil
}

// shared helpers ------------------------------------------------------------

func (s *Service) resolveBackupDir(ctx context.Context, idOrPath string) (string, string, error) {
	value := strings.TrimSpace(idOrPath)
	if value == "" {
		return "", "", httpx.NewError(http.StatusBadRequest, "bad_request", "备份 ID 或路径不能为空。")
	}
	if uuidPattern.MatchString(value) {
		var relPath string
		err := s.db.Pool.QueryRow(ctx, `select rel_path from backups where id = $1::uuid`, value).Scan(&relPath)
		if err == nil {
			dir := filepath.Join(s.cfg.DataDir, filepath.FromSlash(relPath))
			if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
				return dir, value, nil
			}
			return "", "", httpx.NewError(http.StatusNotFound, "backup_not_found", "备份记录存在，但备份目录不存在。")
		}
		if !errors.Is(store.MapError(err), store.ErrNotFound) {
			return "", "", err
		}
	}
	candidates := []string{value}
	if !filepath.IsAbs(value) {
		candidates = append(candidates,
			filepath.Join(s.cfg.DataDir, value),
			filepath.Join(s.cfg.BackupDir(), value),
			filepath.Join(s.cfg.BackupDir(), filepath.Base(value)),
		)
	}
	for _, candidate := range candidates {
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			continue
		}
		if !backupTimestampPattern.MatchString(filepath.Base(abs)) {
			continue
		}
		var rowID string
		rel, relErr := filepath.Rel(s.cfg.DataDir, abs)
		if relErr == nil {
			_ = s.db.Pool.QueryRow(ctx, `select id::text from backups where rel_path = $1`, filepath.ToSlash(rel)).Scan(&rowID)
		}
		return abs, rowID, nil
	}
	return "", "", httpx.NewError(http.StatusNotFound, "backup_not_found", "备份不存在："+value)
}

func readManifest(dir string) (backupManifest, error) {
	var manifest backupManifest
	payload, err := os.ReadFile(filepath.Join(dir, manifestFileName))
	if err != nil {
		return manifest, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func verifyFileHash(path, wantSHA string, wantSize int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if wantSize >= 0 && info.Size() != wantSize {
		return fmt.Errorf("大小不一致：期望 %d，实际 %d", wantSize, info.Size())
	}
	if wantSHA == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != wantSHA {
		return fmt.Errorf("sha256 不一致：期望 %s，实际 %s", wantSHA, actual)
	}
	return nil
}

func (s *Service) renderRestoreReadme(manifest backupManifest, relPath string) (string, error) {
	conn, err := parseConnInfo(s.cfg.DatabaseURL)
	hint := "postgres://<用户>:<密码>@<主机>:<端口>/<新数据库>"
	if err == nil {
		hint = conn.dsnHint("<新数据库>")
	}
	var builder strings.Builder
	builder.WriteString("# RKA 备份恢复说明\n\n")
	builder.WriteString("本目录由 `rka backup` / `rka restore` 生成。\n\n")
	builder.WriteString("如果本目录是备份批次（包含 `db.dump` 与 `manifest.json`，尚未经过 `rka restore`），请先执行 `rka restore <备份 id 或路径> <新目录>` 生成恢复数据根，再按下面步骤恢复数据库。\n\n")
	builder.WriteString("| 项目 | 值 |\n| --- | --- |\n")
	builder.WriteString("| 备份批次 | `" + relPath + "` |\n")
	builder.WriteString("| 创建时间 | " + manifest.CreatedAt.UTC().Format(time.RFC3339) + " |\n")
	builder.WriteString(fmt.Sprintf("| 二进制 schema 版本 | %d |\n", manifest.BinaryVersion))
	builder.WriteString(fmt.Sprintf("| 数据库 schema 版本 | %d |\n", manifest.DBSchemaVersion))
	builder.WriteString("| 内容版本 | `" + manifest.ContentVersion + "` |\n")
	builder.WriteString("| 数据库 dump | `db.dump`（custom 格式，sha256 `" + manifest.DBDumpSHA256 + "`） |\n")
	builder.WriteString(fmt.Sprintf("| 原件 | `originals/`（%d 个文件） |\n", len(manifest.Originals)))
	secretsIncluded, _ := manifest.Secrets["included"].(bool)
	if secretsIncluded {
		builder.WriteString("| 应用密钥 | `secrets/secret-key`（已包含，敏感数据） |\n")
	} else {
		builder.WriteString("| 应用密钥 | 未包含（需单独提供 RKA_SECRET_KEY） |\n")
	}
	builder.WriteString("\n")
	if secretsIncluded {
		builder.WriteString("> **敏感数据警告**：本目录包含应用密钥 `secrets/secret-key`，可解密模型凭据与教学身份密文。请按密钥保管备份与恢复目录，不要提交到公开仓库或普通共享。\n\n")
	}
	builder.WriteString("## 恢复步骤\n\n")
	builder.WriteString("1. 创建一个**空数据库**（不要指向现有生产库），例如 `CREATE DATABASE rka_restored;`。\n")
	builder.WriteString("2. 导入数据库 dump：\n\n")
	builder.WriteString("   ```bash\n")
	builder.WriteString("   pg_restore --no-owner --no-privileges --single-transaction \\\n")
	builder.WriteString("     --dbname \"" + hint + "\" db.dump\n")
	builder.WriteString("   ```\n\n")
	builder.WriteString("3. 使用恢复目录作为数据根启动应用（示例）：\n\n")
	builder.WriteString("   ```bash\n")
	builder.WriteString("   RKA_DATA_DIR=<本目录> \\\n")
	builder.WriteString("   RKA_DATABASE_URL=\"" + hint + "\" \\\n")
	builder.WriteString("   RKA_ADDR=127.0.0.1:8081 \\\n")
	builder.WriteString("   rka serve\n")
	builder.WriteString("   ```\n\n")
	if secretsIncluded {
		builder.WriteString("4. 备份已包含应用密钥：启动时设置 `RKA_SECRET_KEY_FILE=<本目录>/secrets/secret-key`；该密钥可解密模型凭据与教学身份密文，请按密钥保管。\n")
	} else {
		builder.WriteString("4. 备份未包含应用密钥：请单独提供 `RKA_SECRET_KEY` 或 `RKA_SECRET_KEY_FILE`，否则模型凭据密文与教学身份无法解密。\n")
	}
	builder.WriteString("5. 访问 `/readyz` 与 `/api/admin/status`，确认 schema 版本、内容与依赖状态。\n\n")
	builder.WriteString("## 行数统计（备份时）\n\n")
	builder.WriteString("| 表 | 行数 |\n| --- | --- |\n")
	tables := make([]string, 0, len(manifest.Tables))
	for table := range manifest.Tables {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	for _, table := range tables {
		builder.WriteString(fmt.Sprintf("| %s | %d |\n", table, manifest.Tables[table]))
	}
	builder.WriteString("\n原件的 sha256 与相对路径记录在 `manifest.json` 的 `originals` 字段。\n")
	return builder.String(), nil
}

func sha256Hex(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func fileSHA256(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return ""
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func directorySize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

func truncate(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max] + "…"
}

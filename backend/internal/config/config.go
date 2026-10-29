// Package config loads every runtime setting from the environment.
// No setting is read from the repository tree, so one image runs in any
// deployment and the same Compose definition can be reused on another machine.
package config

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Mode      string
	Addr      string
	DataDir   string
	StaticDir string
	// ContentSnapshot is the bundled content snapshot used by `rka init` /
	// `rka import-content`. It is never served as static web content.
	ContentSnapshot string
	DatabaseURL     string
	SecretKey       []byte
	SessionTTL      time.Duration
	CookieName      string
	// CookieSecure is "auto", "always" or "never". "auto" derives the flag from
	// the request scheme so plain HTTP on a LAN keeps working while TLS gets
	// Secure cookies.
	CookieSecure   string
	TrustProxy     bool
	BootstrapToken string
	LogLevel       string
	LogFormat      string
	RequestTimeout time.Duration

	Worker    WorkerConfig
	AI        AIConfig
	Teaching  TeachingConfig
	Retention RetentionConfig
}

type WorkerConfig struct {
	Enabled        bool
	ID             string
	PollInterval   time.Duration
	LeaseTTL       time.Duration
	Heartbeat      time.Duration
	MaxAttempts    int
	MaxConcurrency int
	Inline         bool
}

type AIConfig struct {
	Endpoint       string
	Model          string
	APIKey         string
	GatewayName    string
	MaxConcurrency int
	DailyBudget    int
	RequestTimeout time.Duration
}

type TeachingConfig struct {
	// DeepTutor is the independent teaching service. When the base URL is empty
	// the teaching adapter reports itself unavailable instead of pretending.
	DeepTutorURL string
	// DeepTutor has no static API token in the pinned version; the adapter
	// signs in as an administrator and caches per-user JWTs. DeepTutorToken is
	// kept only for test doubles and legacy deployments.
	DeepTutorToken         string
	DeepTutorAdminUsername string
	DeepTutorAdminPassword string
	EmbeddingURL           string
	EmbeddingModel         string
	EmbeddingDims          int
}

type RetentionConfig struct {
	TaskDays   int
	EventDays  int
	AuditDays  int
	BackupDays int
	LogDays    int
}

func Load() (*Config, error) {
	cfg := &Config{
		Mode:            env("RKA_MODE", "serve"),
		Addr:            env("RKA_ADDR", "127.0.0.1:8080"),
		DataDir:         env("RKA_DATA_DIR", "./data"),
		StaticDir:       env("RKA_STATIC_DIR", "./dist"),
		ContentSnapshot: env("RKA_CONTENT_SNAPSHOT", "./content"),
		DatabaseURL:     env("RKA_DATABASE_URL", "postgres://rka:rka@127.0.0.1:5432/rka?sslmode=disable"),
		SessionTTL:      envDuration("RKA_SESSION_TTL", 30*24*time.Hour),
		CookieName:      env("RKA_COOKIE_NAME", "rka_session"),
		CookieSecure:    strings.ToLower(env("RKA_COOKIE_SECURE", "auto")),
		TrustProxy:      envBool("RKA_TRUST_PROXY", false),
		BootstrapToken:  os.Getenv("RKA_BOOTSTRAP_TOKEN"),
		LogLevel:        strings.ToLower(env("RKA_LOG_LEVEL", "info")),
		LogFormat:       strings.ToLower(env("RKA_LOG_FORMAT", "json")),
		RequestTimeout:  envDuration("RKA_REQUEST_TIMEOUT", 30*time.Second),
		Worker: WorkerConfig{
			Enabled:        envBool("RKA_WORKER_ENABLED", true),
			ID:             env("RKA_WORKER_ID", ""),
			PollInterval:   envDuration("RKA_WORKER_POLL", 2*time.Second),
			LeaseTTL:       envDuration("RKA_WORKER_LEASE_TTL", 60*time.Second),
			Heartbeat:      envDuration("RKA_WORKER_HEARTBEAT", 15*time.Second),
			MaxAttempts:    envInt("RKA_WORKER_MAX_ATTEMPTS", 3),
			MaxConcurrency: envInt("RKA_WORKER_CONCURRENCY", 2),
			Inline:         envBool("RKA_WORKER_INLINE", false),
		},
		AI: AIConfig{
			Endpoint:       os.Getenv("RKA_AI_ENDPOINT"),
			Model:          os.Getenv("RKA_AI_MODEL"),
			APIKey:         os.Getenv("RKA_AI_API_KEY"),
			GatewayName:    env("RKA_AI_GATEWAY_NAME", "服务端 AI 网关"),
			MaxConcurrency: envInt("RKA_AI_MAX_CONCURRENCY", 3),
			DailyBudget:    envInt("RKA_AI_DAILY_BUDGET", 0),
			RequestTimeout: envDuration("RKA_AI_TIMEOUT", 120*time.Second),
		},
		Teaching: TeachingConfig{
			DeepTutorURL:           os.Getenv("RKA_DEEPTUTOR_URL"),
			DeepTutorToken:         os.Getenv("RKA_DEEPTUTOR_TOKEN"),
			DeepTutorAdminUsername: env("RKA_DEEPTUTOR_ADMIN_USERNAME", "rka-admin"),
			DeepTutorAdminPassword: os.Getenv("RKA_DEEPTUTOR_ADMIN_PASSWORD"),
			EmbeddingURL:           os.Getenv("RKA_EMBEDDING_URL"),
			EmbeddingModel:         env("RKA_EMBEDDING_MODEL", "Xenova/bge-small-zh-v1.5"),
			EmbeddingDims:          envInt("RKA_EMBEDDING_DIMS", 512),
		},
		Retention: RetentionConfig{
			TaskDays:   envInt("RKA_RETENTION_TASK_DAYS", 90),
			EventDays:  envInt("RKA_RETENTION_EVENT_DAYS", 30),
			AuditDays:  envInt("RKA_RETENTION_AUDIT_DAYS", 365),
			BackupDays: envInt("RKA_RETENTION_BACKUP_DAYS", 30),
			LogDays:    envInt("RKA_RETENTION_LOG_DAYS", 30),
		},
	}
	abs, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	cfg.DataDir = abs
	// `rka init` writes the one-time bootstrap token into the project data root
	// (data/secrets/bootstrap-token, 0600). Reading it here keeps the documented
	// deployment flow working without copying the secret into the container
	// environment; an explicit RKA_BOOTSTRAP_TOKEN still takes precedence.
	if cfg.BootstrapToken == "" {
		token, err := readBootstrapToken(cfg.SecretsDir())
		if err != nil {
			return nil, err
		}
		cfg.BootstrapToken = token
	}
	if cfg.Worker.ID == "" {
		cfg.Worker.ID = defaultWorkerID()
	}
	key, err := loadSecretKey()
	if err != nil {
		return nil, err
	}
	cfg.SecretKey = key
	if cfg.Teaching.DeepTutorURL != "" && cfg.Teaching.DeepTutorAdminPassword == "" {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte("rka-deeptutor-admin-v1"))
		cfg.Teaching.DeepTutorAdminPassword = hex.EncodeToString(mac.Sum(nil))
	}
	if cfg.CookieSecure != "auto" && cfg.CookieSecure != "always" && cfg.CookieSecure != "never" {
		return nil, fmt.Errorf("RKA_COOKIE_SECURE must be auto, always or never")
	}
	if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
		return nil, fmt.Errorf("RKA_ADDR %q: %w", cfg.Addr, err)
	}
	return cfg, nil
}

// DataPath joins a path under the project data root and refuses to escape it.
func (c *Config) DataPath(parts ...string) string {
	p := filepath.Join(append([]string{c.DataDir}, parts...)...)
	root := filepath.Clean(c.DataDir) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(p)+string(os.PathSeparator), root) && filepath.Clean(p) != filepath.Clean(c.DataDir) {
		panic("data path escapes data root: " + p)
	}
	return p
}

func (c *Config) BackupDir() string    { return c.DataPath("backups") }
func (c *Config) OriginalsDir() string { return c.DataPath("originals") }
func (c *Config) DerivedDir() string   { return c.DataPath("derived") }
func (c *Config) ExportDir() string    { return c.DataPath("exports") }
func (c *Config) LogDir() string       { return c.DataPath("logs") }
func (c *Config) SecretsDir() string   { return c.DataPath("secrets") }
func (c *Config) TeachingDir() string  { return c.DataPath("teaching") }
func (c *Config) CacheDir() string     { return c.DataPath("cache") }
func (c *Config) GatewayDir() string   { return c.DataPath("gateway") }

// EnsureDirs creates the managed data layout. It only touches the directories
// it owns and never rewrites ownership of the whole project.
func (c *Config) EnsureDirs() error {
	dirs := []string{
		c.DataDir, c.BackupDir(), c.OriginalsDir(), c.DerivedDir(), c.ExportDir(),
		c.LogDir(), c.SecretsDir(), c.TeachingDir(), c.CacheDir(), c.GatewayDir(),
		filepath.Join(c.DataDir, "postgres"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return parsed
}

func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	parsed, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return parsed
}

func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	parsed, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return parsed
}

func defaultWorkerID() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "worker"
	}
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return host + "-" + hex.EncodeToString(buf)
}

// loadSecretKey reads the server secret from RKA_SECRET_KEY or from the
// project-local secrets file. `rka init` may create it on first run.
func loadSecretKey() ([]byte, error) {
	autogenerate := strings.EqualFold(os.Getenv("RKA_SECRET_KEY_AUTOGENERATE"), "true")
	if raw := strings.TrimSpace(os.Getenv("RKA_SECRET_KEY")); raw != "" {
		key, err := hex.DecodeString(raw)
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("RKA_SECRET_KEY must be 64 hex characters (32 bytes)")
		}
		return key, nil
	}
	path := strings.TrimSpace(os.Getenv("RKA_SECRET_KEY_FILE"))
	if path == "" {
		path = filepath.Join(env("RKA_DATA_DIR", "./data"), "secrets", "secret-key")
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		key, decodeErr := hex.DecodeString(strings.TrimSpace(string(data)))
		if decodeErr != nil || len(key) != 32 {
			return nil, fmt.Errorf("%s exists but is not a valid 32-byte hex key", path)
		}
		return key, nil
	case !os.IsNotExist(err):
		return nil, fmt.Errorf("read secret key file: %w", err)
	case autogenerate:
		return generateSecretKey(path)
	default:
		return nil, fmt.Errorf("secret key missing at %s. Run `rka init` (or `rka init-secrets`) on first deployment, or set RKA_SECRET_KEY / RKA_SECRET_KEY_FILE", path)
	}
}

// readBootstrapToken reads the one-time initialisation token created by
// `rka init`. A missing file is normal (the deployment may configure an
// administrator through RKA_ADMIN_EMAIL instead); an unreadable file is
// returned as an error so the bootstrap entry point cannot silently report
// itself disabled while a token exists on disk.
func readBootstrapToken(secretsDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(secretsDir, "bootstrap-token"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read bootstrap token file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func generateSecretKey(path string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create secrets dir: %w", err)
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	encoded := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write secret key: %w", err)
	}
	return buf, nil
}

// writeSecretKeyFile creates data/secrets/secret-key once, with owner-only
// permissions. It is only used by the explicit initialisation command.
func writeSecretKeyFile() ([]byte, error) {
	path := filepath.Join(env("RKA_DATA_DIR", "./data"), "secrets", "secret-key")
	if data, err := os.ReadFile(path); err == nil {
		key, decodeErr := hex.DecodeString(strings.TrimSpace(string(data)))
		if decodeErr != nil || len(key) != 32 {
			return nil, fmt.Errorf("%s exists but is not a valid 32-byte hex key", path)
		}
		return key, nil
	}
	return generateSecretKey(path)
}

// GenerateBootstrapToken creates data/secrets/bootstrap-token once and returns
// its path. The token value is never logged.
func GenerateBootstrapToken() (string, error) {
	dataDir := env("RKA_DATA_DIR", "./data")
	dir := filepath.Join(dataDir, "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create secrets dir: %w", err)
	}
	path := filepath.Join(dir, "bootstrap-token")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	value := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write bootstrap token: %w", err)
	}
	return path, nil
}

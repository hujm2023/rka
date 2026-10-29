package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeBootstrapToken mirrors what `rka init` does: create the one-time token
// under the project data root with owner-only permissions.
func writeBootstrapToken(t *testing.T, dataDir, token string) {
	t.Helper()
	dir := filepath.Join(dataDir, "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create secrets dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bootstrap-token"), []byte(token+"\n"), 0o600); err != nil {
		t.Fatalf("write bootstrap token: %v", err)
	}
}

func TestLoadReadsBootstrapTokenFromDataRoot(t *testing.T) {
	dataDir := t.TempDir()
	writeBootstrapToken(t, dataDir, "file-token")
	t.Setenv("RKA_DATA_DIR", dataDir)
	t.Setenv("RKA_SECRET_KEY_AUTOGENERATE", "true")
	t.Setenv("RKA_BOOTSTRAP_TOKEN", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.BootstrapToken != "file-token" {
		t.Fatalf("bootstrap token = %q, want the file token", cfg.BootstrapToken)
	}
}

func TestLoadBootstrapTokenEnvironmentWins(t *testing.T) {
	dataDir := t.TempDir()
	writeBootstrapToken(t, dataDir, "file-token")
	t.Setenv("RKA_DATA_DIR", dataDir)
	t.Setenv("RKA_SECRET_KEY_AUTOGENERATE", "true")
	t.Setenv("RKA_BOOTSTRAP_TOKEN", "env-token")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.BootstrapToken != "env-token" {
		t.Fatalf("bootstrap token = %q, want the environment token", cfg.BootstrapToken)
	}
}

func TestLoadWithoutBootstrapTokenFileIsNotAnError(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("RKA_DATA_DIR", dataDir)
	t.Setenv("RKA_SECRET_KEY_AUTOGENERATE", "true")
	t.Setenv("RKA_BOOTSTRAP_TOKEN", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.BootstrapToken != "" {
		t.Fatalf("bootstrap token = %q, want empty", cfg.BootstrapToken)
	}
}

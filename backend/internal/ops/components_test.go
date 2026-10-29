package ops

import (
	"os"
	"path/filepath"
	"testing"

	"rka/internal/config"
)

func TestNativeComponentsSnapshotRestoreAndTamper(t *testing.T) {
	cfg := &config.Config{DataDir: t.TempDir(), Mode: "backup"}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	s := &Service{cfg: cfg}
	for _, dir := range []string{cfg.TeachingDir(), cfg.GatewayDir()} {
		if err := os.WriteFile(filepath.Join(dir, "native.json"), []byte(`{"state":"preserve"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.requireComponentQuiescence(); err == nil {
		t.Fatal("live native state was accepted without quiescence")
	}
	t.Setenv("RKA_BACKUP_QUIESCED", "true")
	if err := s.requireComponentQuiescence(); err != nil {
		t.Fatal(err)
	}
	cfg.Mode = "serve"
	if err := s.requireComponentQuiescence(); err == nil {
		t.Fatal("HTTP server trusted offline-only flag")
	}
	cfg.Mode = "backup"
	backup := t.TempDir()
	components, err := s.copyComponents(t.Context(), backup)
	if err != nil {
		t.Fatal(err)
	}
	manifest := backupManifest{Components: components}
	restored := t.TempDir()
	if err := restoreComponents(backup, restored, manifest); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"teaching", "gateway"} {
		data, err := os.ReadFile(filepath.Join(restored, name, "native.json"))
		if err != nil || string(data) != `{"state":"preserve"}` {
			t.Fatalf("%s lost native state: %s %v", name, data, err)
		}
	}
	if err := os.WriteFile(filepath.Join(backup, "components", "teaching", "native.json"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := componentFiles(backup, manifest, nil); err == nil {
		t.Fatal("corrupt native snapshot passed verification")
	}
	if err := restoreComponents(backup, t.TempDir(), manifest); err == nil {
		t.Fatal("corrupt native snapshot was restored")
	}
}

func TestInventoryOnlyIsNotACompleteBackup(t *testing.T) {
	manifest := backupManifest{Teaching: map[string]any{"status": "inventory_only"}}
	if err := componentFiles(t.TempDir(), manifest, nil); err == nil {
		t.Fatal("inventory-only backup accepted as complete")
	}
}

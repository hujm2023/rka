package worker

import (
	"os"
	"testing"
	"time"

	"rka/internal/config"
)

func TestHealthRequiresRecentWorkerHeartbeat(t *testing.T) {
	cfg := &config.Config{DataDir: t.TempDir()}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if CheckHealth(cfg) == nil {
		t.Fatal("unstarted worker reported healthy")
	}
	if err := recordHeartbeat(cfg); err != nil {
		t.Fatal(err)
	}
	if err := CheckHealth(cfg); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	if err := os.Chtimes(heartbeatPath(cfg), past, past); err != nil {
		t.Fatal(err)
	}
	if CheckHealth(cfg) == nil {
		t.Fatal("stalled worker reported healthy")
	}
}

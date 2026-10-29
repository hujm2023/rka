package worker

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"rka/internal/config"
)

func heartbeatPath(cfg *config.Config) string {
	host, _ := os.Hostname()
	return filepath.Join(cfg.LogDir(), "worker-"+filepath.Base(host)+".heartbeat")
}

func recordHeartbeat(cfg *config.Config) error {
	return os.WriteFile(heartbeatPath(cfg), []byte("polling\n"), 0600)
}

// CheckHealth tests this container's polling heartbeat. Workers have no HTTP
// listener, so the application's HTTP health check cannot describe their health.
func CheckHealth(cfg *config.Config) error {
	info, err := os.Stat(heartbeatPath(cfg))
	if err != nil {
		return fmt.Errorf("worker heartbeat unavailable: %w", err)
	}
	limit := max(30*time.Second, 3*cfg.Worker.PollInterval)
	if time.Since(info.ModTime()) > limit {
		return fmt.Errorf("worker has not polled within %s", limit)
	}
	return nil
}

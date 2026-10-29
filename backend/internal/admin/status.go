package admin

import (
	"net/http"

	"rka/internal/httpx"
)

// handleStatus returns the same independently-reported health signals used by
// ops.Health: process, database, teaching, model and index, plus versions.
// Both the flat component keys and the nested "components" object are emitted
// so dashboards can consume either shape.
func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	report := s.ops.Health(r.Context())
	components := map[string]any{
		"process": report.Process, "database": report.Database,
		"teaching": report.Teaching, "model": report.Model, "index": report.Index,
	}
	checks := map[string]string{
		"process":  report.Process.Status,
		"database": report.Database.Status,
		"teaching": report.Teaching.Status,
		"model":    report.Model.Status,
		"index":    report.Index.Status,
	}
	// Frontend contract: any component that is not ok/ready/unconfigured makes
	// the platform degraded. Unconfigured optional dependencies do not.
	status := "ok"
	for _, value := range checks {
		if value != "ok" && value != "ready" && value != "unconfigured" {
			status = "degraded"
		}
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"status":     status,
		"checks":     checks,
		"process":    report.Process,
		"database":   report.Database,
		"teaching":   report.Teaching,
		"model":      report.Model,
		"index":      report.Index,
		"components": components,
		"version":    report.Version,
		"checkedAt":  report.CheckedAt,
	})
}

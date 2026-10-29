// Package ops owns operational concerns that are not tied to one business
// domain: health reporting, consistency backups, restore rehearsals, retention,
// personal data export/deletion and legacy sync import.
package ops

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	"rka/internal/audit"
	"rka/internal/auth"
	"rka/internal/config"
	"rka/internal/httpx"
	"rka/internal/store"
)

type Deps struct {
	Config *config.Config
	DB     *store.DB
	Log    *slog.Logger
	Auth   *auth.Service
	Audit  *audit.Recorder
}

type Service struct {
	cfg       *config.Config
	db        *store.DB
	log       *slog.Logger
	auth      *auth.Service
	audit     *audit.Recorder
	startedAt time.Time

	// backupMu serialises backup batches so two requests cannot write into the
	// same timestamp directory or race on pg_dump resource usage.
	backupMu sync.Mutex
}

func New(deps Deps) *Service {
	return &Service{
		cfg:       deps.Config,
		db:        deps.DB,
		log:       deps.Log,
		auth:      deps.Auth,
		audit:     deps.Audit,
		startedAt: time.Now(),
	}
}

// Register mounts the sync, data-autonomy and backup/retention routes. Health
// probes (/healthz, /readyz) are owned by internal/app and are not repeated
// here.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sync/state", s.auth.Require(s.handleSyncState))
	mux.HandleFunc("POST /api/sync/import", s.auth.Require(s.handleSyncImport))
	mux.HandleFunc("GET /api/export/me", s.auth.Require(s.handleExportMe))
	mux.HandleFunc("DELETE /api/me/data", s.auth.Require(s.handleDeleteMyData))

	mux.HandleFunc("GET /api/admin/backups", s.auth.RequireAdmin(s.handleListBackups))
	mux.HandleFunc("POST /api/admin/backups", s.auth.RequireAdmin(s.handleCreateBackup))
	mux.HandleFunc("POST /api/admin/backups/{id}/verify", s.auth.RequireAdmin(s.handleVerifyBackup))
	mux.HandleFunc("POST /api/admin/backups/{id}/restore", s.auth.RequireAdmin(s.handleRestoreBackup))
	mux.HandleFunc("GET /api/admin/retention", s.auth.RequireAdmin(s.handleGetRetention))
	mux.HandleFunc("PUT /api/admin/retention", s.auth.RequireAdmin(s.handlePutRetention))
}

// record writes an audit entry attributed to the current request actor.
func (s *Service) record(r *http.Request, action, targetType, targetID string, detail map[string]any) {
	actor := auth.MustUser(r.Context())
	s.audit.Record(r.Context(), audit.Entry{
		ActorID:    actor.ID,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Detail:     detail,
		RequestID:  httpx.RequestIDFrom(r.Context()),
		IP:         httpx.ClientIP(r, s.cfg.TrustProxy),
	})
}

// Backup describes one consistency backup set as exposed to operators.
type Backup struct {
	ID        string    `json:"id"`
	RelPath   string    `json:"relPath"`
	Path      string    `json:"path"`
	Size      int64     `json:"sizeBytes"`
	SHA256    string    `json:"sha256"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

// VerifyReport is the result of a restore rehearsal check. Every check and
// error is a human-readable string so the admin UI can show exactly what was
// verified instead of a single boolean.
type VerifyReport struct {
	ID         string    `json:"id"`
	OK         bool      `json:"ok"`
	Checks     []string  `json:"checks"`
	Errors     []string  `json:"errors"`
	Warnings   []string  `json:"warnings,omitempty"`
	VerifiedAt time.Time `json:"verifiedAt"`
}

// Package admin owns operator-facing HTTP endpoints: status, background task
// observation, audit log, model configuration management and metrics. It sits
// above ops and never hides an unconfigured or failing dependency.
package admin

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"rka/internal/ai"
	"rka/internal/audit"
	"rka/internal/auth"
	"rka/internal/config"
	"rka/internal/httpx"
	"rka/internal/ops"
	"rka/internal/store"
	"rka/internal/teaching"
)

type Deps struct {
	Config   *config.Config
	DB       *store.DB
	Log      *slog.Logger
	Auth     *auth.Service
	AI       *ai.Service
	Ops      *ops.Service
	Teaching *teaching.Service
}

type pgxTx = pgx.Tx

type Service struct {
	cfg      *config.Config
	db       *store.DB
	log      *slog.Logger
	auth     *auth.Service
	ai       *ai.Service
	ops      *ops.Service
	teaching *teaching.Service
	audit    *audit.Recorder

	startedAt time.Time
}

func New(deps Deps) *Service {
	return &Service{
		cfg:       deps.Config,
		db:        deps.DB,
		log:       deps.Log,
		auth:      deps.Auth,
		ai:        deps.AI,
		ops:       deps.Ops,
		teaching:  deps.Teaching,
		audit:     audit.New(deps.DB, deps.Log),
		startedAt: time.Now(),
	}
}

// Register mounts the admin observation and model configuration routes.
// /api/admin/users and /api/admin/grants are owned by internal/auth and
// /api/admin/backups + /api/admin/retention by internal/ops, so they are not
// registered here.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/status", s.auth.RequireAdmin(s.handleStatus))
	mux.HandleFunc("GET /api/admin/tasks", s.auth.RequireAdmin(s.handleTasks))
	mux.HandleFunc("GET /api/admin/audit", s.auth.RequireAdmin(s.handleAuditLog))
	mux.HandleFunc("GET /api/admin/model-configs", s.auth.RequireAdmin(s.handleListModelConfigs))
	mux.HandleFunc("POST /api/admin/model-configs", s.auth.RequireAdmin(s.handleCreateModelConfig))
	mux.HandleFunc("PUT /api/admin/model-configs/{id}", s.auth.RequireAdmin(s.handleUpdateModelConfig))
	mux.HandleFunc("POST /api/admin/model-configs/{id}/check", s.auth.RequireAdmin(s.handleCheckModelConfig))
	mux.HandleFunc("GET /api/admin/metrics", s.auth.RequireAdmin(s.handleMetrics))
}

// record writes an audit entry for an admin action.
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

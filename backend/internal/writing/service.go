package writing

import (
	"context"
	"log/slog"
	"net/http"

	"rka/internal/ai"
	"rka/internal/auth"
	"rka/internal/config"
	"rka/internal/store"
)

// Deps is the wiring contract for the package. Keep this struct stable: the
// application assembly and integration tests both construct services from it.
type Deps struct {
	Config *config.Config
	DB     *store.DB
	Log    *slog.Logger
	Auth   *auth.Service
	// AI creates teaching tasks. It is an interface so writing can be tested
	// without a running model gateway.
	AI TaskCreator
}

// TaskCreator is the part of ai.Service that writing depends on.
type TaskCreator interface {
	CreateTask(ctx context.Context, in ai.CreateTaskInput) (ai.Task, error)
}

type Service struct {
	cfg  *config.Config
	db   *store.DB
	log  *slog.Logger
	auth *auth.Service
	ai   TaskCreator
}

func New(deps Deps) *Service {
	return &Service{cfg: deps.Config, db: deps.DB, log: deps.Log, auth: deps.Auth, ai: deps.AI}
}

// Register installs the HTTP routes owned by this package.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/writing/{kind}/{targetId}", s.auth.Require(s.handleGetDocument))
	mux.HandleFunc("PUT /api/writing/{kind}/{targetId}", s.auth.Require(s.handlePutDocument))
	mux.HandleFunc("GET /api/writing/{kind}/{targetId}/versions", s.auth.Require(s.handleListVersions))
	mux.HandleFunc("GET /api/writing/{kind}/{targetId}/versions/{version}", s.auth.Require(s.handleGetVersion))
	mux.HandleFunc("POST /api/writing/{kind}/{targetId}/reviews", s.auth.Require(s.handleCreateReview))
	mux.HandleFunc("GET /api/writing/{kind}/{targetId}/reviews", s.auth.Require(s.handleListReviews))
}

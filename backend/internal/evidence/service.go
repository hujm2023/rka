// Package evidence owns the learner-facing learning evidence surface: reading
// progress, self-declarations, mastery derivation, review planning and the
// learning report.
//
// Design boundaries:
//   - attempts are immutable facts owned by the practice package; this package
//     only reads them and never rewrites them;
//   - derived state (mastery, review plan, report) is recomputed from facts on
//     every query, so a failed derived write never invalidates a saved answer;
//   - evidence rows written here (reading, self_declared) live in their own
//     table and are append-only apart from the reading-progress state cache;
//   - no calibrated pass rate is ever produced: the raw counts and the evidence
//     behind each statement are returned instead.
package evidence

import (
	"log/slog"
	"net/http"

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
}

type Service struct {
	cfg  *config.Config
	db   *store.DB
	log  *slog.Logger
	auth *auth.Service
}

func New(deps Deps) *Service {
	return &Service{cfg: deps.Config, db: deps.DB, log: deps.Log, auth: deps.Auth}
}

// Register installs the HTTP routes owned by this package.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/learning/summary", s.auth.Require(s.handleSummary))
	mux.HandleFunc("GET /api/learning/report", s.auth.Require(s.handleReport))
	mux.HandleFunc("GET /api/learning/mastery", s.auth.Require(s.handleMastery))
	mux.HandleFunc("GET /api/learning/review-plan", s.auth.Require(s.handleReviewPlan))
	mux.HandleFunc("POST /api/learning/reading", s.auth.Require(s.handleReading))
	mux.HandleFunc("POST /api/learning/self-declared", s.auth.Require(s.handleSelfDeclared))
}

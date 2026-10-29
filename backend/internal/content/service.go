// Package content owns materials, material versions and sections, question
// versions, knowledge points, source links, annotations and the content
// snapshot importer.
//
// Reading rules that every handler in this package must respect:
//   - answers and analyses never leave the server through the catalog or the
//     question endpoints; only /solution returns them, and only after a
//     submitted attempt (or for administrators);
//   - material originals are immutable and content addressed; parsed sections
//     are version scoped, and old references stay bound to their version;
//   - import failures are visible on the job record instead of being skipped.
package content

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
	// Reader-facing content endpoints. Every one of them requires a session;
	// material-scoped endpoints additionally check auth.CanAccessMaterial.
	mux.HandleFunc("GET /api/content/catalog", s.auth.Require(s.handleCatalog))
	mux.HandleFunc("GET /api/content/questions", s.auth.Require(s.handleListQuestions))
	mux.HandleFunc("GET /api/content/questions/{id}", s.auth.Require(s.handleGetQuestion))
	mux.HandleFunc("GET /api/content/questions/{id}/solution", s.auth.Require(s.handleQuestionSolution))
	mux.HandleFunc("GET /api/content/materials", s.auth.Require(s.handleListMaterials))
	mux.HandleFunc("GET /api/content/materials/{id}", s.auth.Require(s.handleGetMaterial))
	mux.HandleFunc("GET /api/content/materials/{id}/versions", s.auth.Require(s.handleMaterialVersions))
	mux.HandleFunc("GET /api/content/materials/{id}/raw", s.auth.Require(s.handleMaterialRaw))
	mux.HandleFunc("GET /api/content/materials/{id}/content", s.auth.Require(s.handleMaterialContent))
	mux.HandleFunc("GET /api/content/lessons/{id}", s.auth.Require(s.handleLesson))
	mux.HandleFunc("GET /api/content/links", s.auth.Require(s.handleLinks))
	mux.HandleFunc("GET /api/content/search", s.auth.Require(s.handleSearch))
	mux.HandleFunc("POST /api/content/annotations", s.auth.Require(s.handleCreateAnnotation))
	mux.HandleFunc("GET /api/content/annotations", s.auth.Require(s.handleListAnnotations))
	mux.HandleFunc("DELETE /api/content/annotations/{id}", s.auth.Require(s.handleDeleteAnnotation))

	// Content administration.
	mux.HandleFunc("POST /api/admin/content/import", s.auth.RequireAdmin(s.handleAdminImport))
	mux.HandleFunc("GET /api/admin/content/jobs", s.auth.RequireAdmin(s.handleAdminJobs))
	mux.HandleFunc("GET /api/admin/content/jobs/{id}", s.auth.RequireAdmin(s.handleAdminJob))
	mux.HandleFunc("PATCH /api/admin/content/links/{id}", s.auth.RequireAdmin(s.handleAdminPatchLink))
	mux.HandleFunc("PATCH /api/admin/content/questions/{id}", s.auth.RequireAdmin(s.handleAdminPatchQuestion))
	mux.HandleFunc("PATCH /api/admin/content/questions/{id}/review", s.auth.RequireAdmin(s.handleAdminReviewQuestion))
	mux.HandleFunc("PATCH /api/admin/content/materials/{id}/review", s.auth.RequireAdmin(s.handleAdminReviewMaterial))
}

// ImportResult summarises one snapshot import.
type ImportResult struct {
	Version   string         `json:"version"`
	Counts    map[string]int `json:"counts"`
	Errors    []string       `json:"errors"`
	Published bool           `json:"published"`
	// Skipped reports that the same version was already imported cleanly and
	// no writes were needed.
	Skipped bool `json:"skipped"`
	// JobID is the jobs row that records this import for administrators.
	JobID string `json:"jobId,omitempty"`
}

package content

import (
	"net/http"

	"rka/internal/auth"
	"rka/internal/httpx"
)

// handleCatalog returns the whole content metadata index in one response, kept
// compatible with the existing browser catalog. Answers and analyses are never
// selected here.
func (s *Service) handleCatalog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.MustUser(ctx)

	version, err := s.CatalogVersion(ctx)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	serverTime, err := s.db.Now(ctx)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	questions, err := s.loadQuestionViews(ctx, "", nil, "", 0)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	materials, err := s.listMaterialViews(ctx, user, "")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	cases, err := s.loadSubjective(ctx, "case")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	essays, err := s.loadSubjective(ctx, "essay")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	links, err := s.loadCatalogLinks(ctx, user)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	lessons, err := s.loadLessons(ctx, user)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"version":    version,
		"serverTime": formatTime(serverTime),
		"questions":  questions,
		"materials":  materials,
		"cases":      cases,
		"essays":     essays,
		// Lessons are legacy short-index entries kept for old deep links and
		// citations; they carry metadata only, never the body.
		"lessons":        lessons,
		"knowledgeLinks": links,
	})
}

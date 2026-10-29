package content

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"rka/internal/httpx"
)

// linkView is one question-to-source-section association. Anchor is the
// version-scoped reader position; SectionKey is the cross-version identity.
type linkView struct {
	ID                string   `json:"id"`
	QuestionID        string   `json:"questionId"`
	MaterialID        string   `json:"materialId"`
	MaterialTitle     string   `json:"materialTitle"`
	MaterialVersionID string   `json:"materialVersionId"`
	Anchor            string   `json:"anchor"`
	SectionID         string   `json:"sectionId"`
	SectionKey        string   `json:"sectionKey"`
	Title             string   `json:"title"`
	SectionTitle      string   `json:"sectionTitle"`
	Path              []string `json:"path"`
	SectionPath       []string `json:"sectionPath"`
	Terms             []string `json:"terms"`
	Score             float64  `json:"score"`
	Status            string   `json:"status"`
}

func (s *Service) handleLinks(w http.ResponseWriter, r *http.Request) {
	questionID := strings.TrimSpace(r.URL.Query().Get("questionId"))
	if questionID == "" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "missing_question", "请提供 questionId。"))
		return
	}
	user := authUser(r)
	var grantUser any
	if !user.IsAdmin() {
		grantUser = user.ID
	}
	rows, err := s.db.Pool.Query(r.Context(), `
		select l.id::text, l.question_id, l.material_id, m.title, l.material_version_id::text,
		       s.anchor, s.stable_key, s.title, s.path, l.terms, l.score, l.status
		from knowledge_links l
		join materials m on m.id = l.material_id
		join material_sections s on s.id = l.section_id
		where l.question_id = $1
		  and l.material_version_id = m.current_version_id
		  and l.status <> 'rejected'
		  and ($2 or m.review_status = 'published'
		       or exists (
		           select 1 from content_grants g
		           where g.user_id = $3::uuid and g.revoked_at is null
		             and g.resource_type = 'material' and g.resource_id = m.id))
		order by l.score desc, l.material_id`, questionID, user.IsAdmin(), grantUser)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	defer rows.Close()
	items := []linkView{}
	for rows.Next() {
		var view linkView
		if err := rows.Scan(&view.ID, &view.QuestionID, &view.MaterialID, &view.MaterialTitle,
			&view.MaterialVersionID, &view.Anchor, &view.SectionKey, &view.Title, &view.Path,
			&view.Terms, &view.Score, &view.Status); err != nil {
			httpx.Error(w, r, err)
			return
		}
		if view.Path == nil {
			view.Path = []string{}
		}
		if view.Terms == nil {
			view.Terms = []string{}
		}
		view.SectionID = view.Anchor
		view.SectionTitle = view.Title
		view.SectionPath = view.Path
		items = append(items, view)
	}
	if err := rows.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"items": items, "nextCursor": ""})
}

type linkPatchRequest struct {
	Status string `json:"status"`
}

func (s *Service) handleAdminPatchLink(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_id", "关联 ID 不正确。"))
		return
	}
	var req linkPatchRequest
	if err := httpx.Decode(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	status := strings.TrimSpace(req.Status)
	if status != "approved" && status != "rejected" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_status",
			"status 只能是 approved 或 rejected。"))
		return
	}
	user := authUser(r)
	tag, err := s.db.Pool.Exec(r.Context(), `
		update knowledge_links
		set status = $2, reviewed_by = $3::uuid, reviewed_at = now()
		where id = $1::uuid`, id, status, user.ID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, r, httpx.ErrNotFound)
		return
	}
	view, err := s.loadLink(r, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.record(r, "content.link.review", "knowledge_link", id, map[string]any{"status": status})
	httpx.JSON(w, r, http.StatusOK, map[string]any{"link": view})
}

func (s *Service) loadLink(r *http.Request, id string) (linkView, error) {
	var view linkView
	err := s.db.Pool.QueryRow(r.Context(), `
		select l.id::text, l.question_id, l.material_id, m.title, l.material_version_id::text,
		       s.anchor, s.stable_key, s.title, s.path, l.terms, l.score, l.status
		from knowledge_links l
		join materials m on m.id = l.material_id
		join material_sections s on s.id = l.section_id
		where l.id = $1::uuid`, id).
		Scan(&view.ID, &view.QuestionID, &view.MaterialID, &view.MaterialTitle,
			&view.MaterialVersionID, &view.Anchor, &view.SectionKey, &view.Title, &view.Path,
			&view.Terms, &view.Score, &view.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return linkView{}, httpx.ErrNotFound
	}
	if err != nil {
		return linkView{}, err
	}
	if view.Path == nil {
		view.Path = []string{}
	}
	if view.Terms == nil {
		view.Terms = []string{}
	}
	view.SectionID = view.Anchor
	view.SectionTitle = view.Title
	view.SectionPath = view.Path
	return view, nil
}

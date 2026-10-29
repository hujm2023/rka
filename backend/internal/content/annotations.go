package content

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"rka/internal/auth"
	"rka/internal/httpx"
)

type annotationView struct {
	ID                string    `json:"id"`
	MaterialID        string    `json:"materialId"`
	MaterialVersionID string    `json:"materialVersionId"`
	SectionAnchor     string    `json:"sectionAnchor"`
	Quote             string    `json:"quote"`
	Note              string    `json:"note"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type annotationRequest struct {
	MaterialID        string `json:"materialId"`
	MaterialVersionID string `json:"materialVersionId"`
	VersionID         string `json:"versionId"`
	SectionID         string `json:"sectionId"`
	SectionAnchor     string `json:"sectionAnchor"`
	Anchor            string `json:"anchor"`
	Quote             string `json:"quote"`
	Note              string `json:"note"`
}

// handleCreateAnnotation stores a user's note or excerpt against an exact
// material version, so historical annotations keep pointing at what the user
// actually read.
func (s *Service) handleCreateAnnotation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.MustUser(ctx)
	var req annotationRequest
	if err := httpx.Decode(w, r, &req, 64<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	materialID := strings.TrimSpace(req.MaterialID)
	if materialID == "" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "missing_material", "请提供 materialId。"))
		return
	}
	if !s.materialReadable(w, r, materialID) {
		return
	}
	quote := strings.TrimSpace(req.Quote)
	note := strings.TrimSpace(req.Note)
	if quote == "" && note == "" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "empty_annotation",
			"批注内容不能为空，请填写摘录或笔记。"))
		return
	}
	if len([]rune(quote)) > 5000 || len([]rune(note)) > 20000 {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "annotation_too_long", "批注内容过长。"))
		return
	}
	versionID := strings.TrimSpace(req.MaterialVersionID)
	if versionID == "" {
		versionID = strings.TrimSpace(req.VersionID)
	}
	if versionID == "" {
		if err := s.db.Pool.QueryRow(ctx,
			`select coalesce(current_version_id::text, '') from materials where id = $1`, materialID).
			Scan(&versionID); err != nil {
			httpx.Error(w, r, err)
			return
		}
		if versionID == "" {
			httpx.Error(w, r, httpx.NewError(http.StatusConflict, "no_version", "该资料还没有可用版本。"))
			return
		}
	} else {
		var belongs bool
		if err := s.db.Pool.QueryRow(ctx,
			`select exists (select 1 from material_versions where id = $1::uuid and material_id = $2)`,
			versionID, materialID).Scan(&belongs); err != nil {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_version", "资料版本不正确。"))
			return
		}
		if !belongs {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_version", "资料版本不属于该资料。"))
			return
		}
	}
	anchor := strings.TrimSpace(req.SectionID)
	if anchor == "" {
		anchor = strings.TrimSpace(req.SectionAnchor)
	}
	if anchor == "" {
		anchor = strings.TrimSpace(req.Anchor)
	}
	if anchor != "" {
		var exists bool
		if err := s.db.Pool.QueryRow(ctx,
			`select exists (select 1 from material_sections where material_version_id = $1::uuid and anchor = $2)`,
			versionID, anchor).Scan(&exists); err != nil {
			httpx.Error(w, r, err)
			return
		}
		if !exists {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_section", "原文小节不存在。"))
			return
		}
	}
	var view annotationView
	err := s.db.Pool.QueryRow(ctx, `
		insert into annotations (user_id, material_id, material_version_id, section_anchor, quote, note)
		values ($1::uuid, $2, $3::uuid, $4, $5, $6)
		returning id::text, material_id, material_version_id::text, section_anchor, quote, note,
		          created_at, updated_at`,
		user.ID, materialID, versionID, anchor, quote, note).
		Scan(&view.ID, &view.MaterialID, &view.MaterialVersionID, &view.SectionAnchor,
			&view.Quote, &view.Note, &view.CreatedAt, &view.UpdatedAt)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view.CreatedAt = view.CreatedAt.UTC()
	view.UpdatedAt = view.UpdatedAt.UTC()
	httpx.JSON(w, r, http.StatusCreated, map[string]any{"annotation": view})
}

func (s *Service) handleListAnnotations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.MustUser(ctx)
	materialID := strings.TrimSpace(r.URL.Query().Get("materialId"))
	rows, err := s.db.Pool.Query(ctx, `
		select id::text, material_id, material_version_id::text, section_anchor, quote, note,
		       created_at, updated_at
		from annotations
		where user_id = $1::uuid and deleted_at is null
		  and ($2 = '' or material_id = $2)
		order by created_at desc
		limit 500`, user.ID, materialID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	defer rows.Close()
	items := []annotationView{}
	for rows.Next() {
		var view annotationView
		if err := rows.Scan(&view.ID, &view.MaterialID, &view.MaterialVersionID, &view.SectionAnchor,
			&view.Quote, &view.Note, &view.CreatedAt, &view.UpdatedAt); err != nil {
			httpx.Error(w, r, err)
			return
		}
		view.CreatedAt = view.CreatedAt.UTC()
		view.UpdatedAt = view.UpdatedAt.UTC()
		items = append(items, view)
	}
	if err := rows.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"items": items, "nextCursor": ""})
}

// handleDeleteAnnotation soft deletes only the caller's own annotation.
func (s *Service) handleDeleteAnnotation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.MustUser(ctx)
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_id", "批注 ID 不正确。"))
		return
	}
	var deleted string
	err := s.db.Pool.QueryRow(ctx, `
		update annotations set deleted_at = now(), updated_at = now()
		where id = $1::uuid and user_id = $2::uuid and deleted_at is null
		returning id::text`, id, user.ID).Scan(&deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, r, httpx.ErrNotFound)
		return
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"ok": true, "id": deleted})
}

package practice

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"rka/internal/auth"
	"rka/internal/httpx"
	"rka/internal/store"
)

type markView struct {
	ID         string    `json:"id"`
	QuestionID string    `json:"questionId"`
	Favorite   bool      `json:"favorite"`
	Uncertain  bool      `json:"uncertain"`
	Note       string    `json:"note"`
	Version    int       `json:"version"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func (s *Service) handleListMarks(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	params, err := parsePage(r, defaultMarksLimit, maxListLimit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	rows, err := s.db.Pool.Query(r.Context(), `
		select question_id, favorite, uncertain, note, version, updated_at
		from marks where user_id = $1::uuid
		order by updated_at desc, question_id asc
		limit $2 offset $3`, user.ID, params.Limit+1, params.Offset)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	defer rows.Close()
	items := []markView{}
	for rows.Next() {
		var item markView
		if err := rows.Scan(&item.ID, &item.Favorite, &item.Uncertain, &item.Note,
			&item.Version, &item.UpdatedAt); err != nil {
			httpx.Error(w, r, err)
			return
		}
		item.QuestionID = item.ID
		item.UpdatedAt = item.UpdatedAt.UTC()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}
	next := ""
	if len(items) > params.Limit {
		items = items[:params.Limit]
		next = encodeCursor(params.Offset + params.Limit)
	}
	httpx.JSON(w, r, http.StatusOK, listResponse{Items: items, NextCursor: next})
}

type putMarkRequest struct {
	Favorite  *bool   `json:"favorite"`
	Uncertain *bool   `json:"uncertain"`
	Note      *string `json:"note"`
	Version   *int    `json:"version"`
}

func (s *Service) handlePutMark(w http.ResponseWriter, r *http.Request) {
	var request putMarkRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	questionID := strings.TrimSpace(r.PathValue("questionId"))
	if questionID == "" {
		httpx.Error(w, r, badRequest("question_required", "缺少题目 ID。"))
		return
	}
	if request.Version == nil {
		httpx.Error(w, r, badRequest("version_required", "缺少版本号，请刷新后重试。"))
		return
	}
	if request.Note != nil && len(*request.Note) > maxNoteLength {
		httpx.Error(w, r, badRequest("note_too_long", "笔记过长。"))
		return
	}
	user := auth.MustUser(r.Context())

	var mark map[string]any
	err := s.db.Tx(r.Context(), func(tx pgx.Tx) error {
		ctx := r.Context()
		var exists bool
		if err := tx.QueryRow(ctx, `select exists (select 1 from questions where id = $1)`, questionID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return httpx.ErrNotFound
		}
		var (
			favorite, uncertain bool
			note                string
			currentVersion      int
			found               bool
		)
		err := tx.QueryRow(ctx, `
			select favorite, uncertain, note, version
			from marks where user_id = $1::uuid and question_id = $2 for update`, user.ID, questionID).
			Scan(&favorite, &uncertain, &note, &currentVersion)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				found = false
			} else {
				return store.MapError(err)
			}
		} else {
			found = true
		}
		if !found {
			// A brand-new mark starts at version 0; the first write creates
			// version 1.
			if *request.Version != 0 && *request.Version != 1 {
				return httpx.ErrConflict.WithDetails(map[string]any{
					"serverVersion": 0,
					"serverValues":  nil,
				})
			}
			if request.Favorite != nil {
				favorite = *request.Favorite
			}
			if request.Uncertain != nil {
				uncertain = *request.Uncertain
			}
			if request.Note != nil {
				note = *request.Note
			}
			var version int
			if err := tx.QueryRow(ctx, `
				insert into marks (user_id, question_id, favorite, uncertain, note, version, updated_at)
				values ($1::uuid, $2, $3, $4, $5, 1, now())
				returning version`, user.ID, questionID, favorite, uncertain, note).Scan(&version); err != nil {
				return fmt.Errorf("insert mark: %w", err)
			}
			mark = markPayload(questionID, favorite, uncertain, note, version)
			return nil
		}
		if currentVersion != *request.Version {
			return httpx.ErrConflict.WithDetails(map[string]any{
				"serverVersion": currentVersion,
				"serverValues":  markPayload(questionID, favorite, uncertain, note, currentVersion),
			})
		}
		if request.Favorite != nil {
			favorite = *request.Favorite
		}
		if request.Uncertain != nil {
			uncertain = *request.Uncertain
		}
		if request.Note != nil {
			note = *request.Note
		}
		var version int
		if err := tx.QueryRow(ctx, `
			update marks set favorite = $3, uncertain = $4, note = $5, version = version + 1, updated_at = now()
			where user_id = $1::uuid and question_id = $2
			returning version`, user.ID, questionID, favorite, uncertain, note).Scan(&version); err != nil {
			return store.MapError(err)
		}
		mark = markPayload(questionID, favorite, uncertain, note, version)
		return nil
	})
	if err != nil {
		if store.IsUniqueViolation(err) {
			// Another device created the same mark first; return the server
			// state as an explicit version conflict.
			var favorite, uncertain bool
			var note string
			var version int
			readErr := s.db.Pool.QueryRow(r.Context(), `
				select favorite, uncertain, note, version
				from marks where user_id = $1::uuid and question_id = $2`, user.ID, questionID).
				Scan(&favorite, &uncertain, &note, &version)
			if readErr == nil {
				httpx.Error(w, r, httpx.ErrConflict.WithDetails(map[string]any{
					"serverVersion": version,
					"serverValues":  markPayload(questionID, favorite, uncertain, note, version),
				}))
				return
			}
		}
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"mark": mark})
}

func markPayload(questionID string, favorite, uncertain bool, note string, version int) map[string]any {
	return map[string]any{
		"id":         questionID,
		"questionId": questionID,
		"favorite":   favorite,
		"uncertain":  uncertain,
		"note":       note,
		"version":    version,
	}
}

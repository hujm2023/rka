package writing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"rka/internal/ai"
	"rka/internal/auth"
	"rka/internal/httpx"
)

// reviewRow is the internal projection of writing_reviews.
type reviewRow struct {
	ID              string
	DocumentID      string
	DocumentVersion int
	TaskID          string
	Status          string
	Feedback        []byte
	CreatedAt       time.Time
	CompletedAt     *time.Time
}

type reviewView struct {
	ID              string          `json:"id"`
	DocumentID      string          `json:"documentId"`
	DocumentVersion int             `json:"documentVersion"`
	TaskID          string          `json:"taskId,omitempty"`
	Status          string          `json:"status"`
	Feedback        json.RawMessage `json:"feedback,omitempty"`
	Stale           bool            `json:"stale"`
	CreatedAt       time.Time       `json:"createdAt"`
	CompletedAt     *time.Time      `json:"completedAt,omitempty"`
}

func (s *Service) viewOfReview(row reviewRow, currentVersion int) reviewView {
	view := reviewView{
		ID:              row.ID,
		DocumentID:      row.DocumentID,
		DocumentVersion: row.DocumentVersion,
		TaskID:          row.TaskID,
		Status:          row.Status,
		Stale:           row.DocumentVersion < currentVersion,
		CreatedAt:       row.CreatedAt.UTC(),
		CompletedAt:     row.CompletedAt,
	}
	if view.CompletedAt != nil {
		utc := view.CompletedAt.UTC()
		view.CompletedAt = &utc
	}
	if len(row.Feedback) > 0 {
		view.Feedback = json.RawMessage(row.Feedback)
	}
	return view
}

func reviewKey(documentID string, version int) string {
	return fmt.Sprintf("writing-review:%s:%d", documentID, version)
}

// mapTaskStatus folds the ai task state machine onto the narrower review status
// vocabulary. The ok result is false for unknown upstream states.
func mapTaskStatus(status string) (string, bool) {
	switch status {
	case "queued":
		return "queued", true
	case "running", "waiting_user", "recovering":
		return "running", true
	case "completed":
		return "completed", true
	case "failed":
		return "failed", true
	case "cancelling", "cancelled":
		return "cancelled", true
	default:
		return "", false
	}
}

func isTerminalReviewStatus(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

// buildReviewContext sends identifiers plus a digest of the draft, never the
// full private text: the worker can load the exact values by document id.
func buildReviewContext(rec documentRecord, kind, targetID string) map[string]any {
	keys := make([]string, 0, len(rec.Values))
	chars := make(map[string]int, len(rec.Values))
	preview := make(map[string]string, len(rec.Values))
	total := 0
	for key, value := range rec.Values {
		keys = append(keys, key)
		length := len([]rune(value))
		chars[key] = length
		total += length
		preview[key] = truncateRunes(value, 200)
	}
	sort.Strings(keys)
	prompt := "请点评这份案例作答：按子问题分别说明已覆盖要点、遗漏或错误、对应原句和具体修改建议，不提供官方分数。"
	if kind == "essay" {
		prompt = "请点评这篇论文草稿：检查题意覆盖、技术准确性、项目角色与前后一致性，结合原文逐段给出修改建议；不要代写整篇，也不要编造项目规模、指标或经历。"
	}
	return map[string]any{
		"documentId":      rec.ID,
		"documentVersion": rec.Version,
		"kind":            kind,
		"targetId":        targetID,
		"reviewKind":      "review_" + kind,
		"prompt":          prompt,
		"values": map[string]any{
			"keys":       keys,
			"chars":      chars,
			"totalChars": total,
			"preview":    preview,
		},
	}
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

type createReviewRequest struct {
	Version *int `json:"version"`
}

func (s *Service) handleCreateReview(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	kind, targetID, err := s.resolveTarget(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req createReviewRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := httpx.Decode(w, r, &req, 8<<10); err != nil {
			httpx.Error(w, r, err)
			return
		}
	}
	rec, found, err := loadDocumentRow(r.Context(), s.db.Pool, user.ID, kind, targetID, false)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !found {
		httpx.Error(w, r, httpx.NewError(http.StatusConflict, "no_document", "请先保存草稿后再发起点评。"))
		return
	}
	if req.Version != nil && *req.Version != rec.Version {
		httpx.Error(w, r, httpx.NewError(http.StatusConflict, "stale_version", "稿件已更新，请基于最新版本重新发起点评。").
			WithDetails(map[string]any{
				"serverVersion":    rec.Version,
				"requestedVersion": *req.Version,
			}))
		return
	}
	// The task idempotency key is derived from document id and version, so a
	// repeated request returns the review that already binds this version.
	if existing, ok, err := s.findReviewByVersion(r.Context(), rec.ID, rec.Version); err != nil {
		httpx.Error(w, r, err)
		return
	} else if ok {
		httpx.JSON(w, r, http.StatusOK, map[string]any{"review": s.viewOfReview(existing, rec.Version)})
		return
	}
	if s.ai == nil {
		httpx.Error(w, r, httpx.NewError(http.StatusServiceUnavailable, "ai_unavailable", "AI 服务未配置，暂时无法发起点评。"))
		return
	}
	task, err := s.ai.CreateTask(r.Context(), ai.CreateTaskInput{
		UserID:         user.ID,
		Kind:           "review_" + kind,
		IdempotencyKey: reviewKey(rec.ID, rec.Version),
		Context:        buildReviewContext(rec, kind, targetID),
	})
	if err != nil {
		s.log.Error("create review task", "error", err, "document_id", rec.ID, "version", rec.Version)
		httpx.Error(w, r, err)
		return
	}
	status := "queued"
	if mapped, ok := mapTaskStatus(task.Status); ok {
		status = mapped
	}
	var (
		reviewID  string
		createdAt time.Time
	)
	var taskID *string
	if task.ID != "" {
		taskID = &task.ID
	}
	err = s.db.Pool.QueryRow(r.Context(), `
		insert into writing_reviews (document_id, document_version, task_id, status)
		values ($1::uuid, $2, $3::uuid, $4)
		returning id::text, created_at`,
		rec.ID, rec.Version, taskID, status).Scan(&reviewID, &createdAt)
	if err != nil {
		httpx.Error(w, r, fmt.Errorf("record writing review: %w", err))
		return
	}
	row := reviewRow{
		ID:              reviewID,
		DocumentID:      rec.ID,
		DocumentVersion: rec.Version,
		TaskID:          task.ID,
		Status:          status,
		CreatedAt:       createdAt,
	}
	httpx.JSON(w, r, http.StatusCreated, map[string]any{"review": s.viewOfReview(row, rec.Version)})
}

func (s *Service) findReviewByVersion(ctx context.Context, documentID string, version int) (reviewRow, bool, error) {
	var (
		row      reviewRow
		feedback []byte
	)
	err := s.db.Pool.QueryRow(ctx, `
		select id::text, document_id::text, document_version, coalesce(task_id::text, ''), status, feedback, created_at, completed_at
		from writing_reviews
		where document_id = $1::uuid and document_version = $2
		order by created_at desc, id desc
		limit 1`, documentID, version).
		Scan(&row.ID, &row.DocumentID, &row.DocumentVersion, &row.TaskID, &row.Status, &feedback, &row.CreatedAt, &row.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return reviewRow{}, false, nil
	}
	if err != nil {
		return reviewRow{}, false, fmt.Errorf("load writing review: %w", err)
	}
	row.Feedback = feedback
	return row, true, nil
}

func (s *Service) handleListReviews(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	kind, targetID, err := s.resolveTarget(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	rec, found, err := loadDocumentRow(r.Context(), s.db.Pool, user.ID, kind, targetID, false)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := []reviewView{}
	if found {
		rows, err := s.db.Pool.Query(r.Context(), `
			select id::text, document_id::text, document_version, coalesce(task_id::text, ''), status, feedback, created_at, completed_at
			from writing_reviews
			where document_id = $1::uuid
			order by created_at desc, id desc`, rec.ID)
		if err != nil {
			httpx.Error(w, r, fmt.Errorf("list writing reviews: %w", err))
			return
		}
		defer rows.Close()
		var reviews []reviewRow
		for rows.Next() {
			var (
				row      reviewRow
				feedback []byte
			)
			if err := rows.Scan(&row.ID, &row.DocumentID, &row.DocumentVersion, &row.TaskID, &row.Status, &feedback, &row.CreatedAt, &row.CompletedAt); err != nil {
				httpx.Error(w, r, fmt.Errorf("scan writing review: %w", err))
				return
			}
			row.Feedback = feedback
			reviews = append(reviews, row)
		}
		if err := rows.Err(); err != nil {
			httpx.Error(w, r, fmt.Errorf("list writing reviews: %w", err))
			return
		}
		if err := s.refreshReviewStatuses(r.Context(), reviews); err != nil {
			s.log.Error("refresh writing review statuses", "error", err)
		}
		for _, row := range reviews {
			items = append(items, s.viewOfReview(row, rec.Version))
		}
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"items":                  items,
		"nextCursor":             "",
		"currentDocumentVersion": rec.Version,
	})
}

// refreshReviewStatuses reads back ai task state so a review does not stay
// "queued" after the worker finished. Feedback is best effort: the review keeps
// whatever it already had when no readable payload exists.
func (s *Service) refreshReviewStatuses(ctx context.Context, reviews []reviewRow) error {
	for i := range reviews {
		row := &reviews[i]
		if row.TaskID == "" {
			continue
		}
		if isTerminalReviewStatus(row.Status) && len(row.Feedback) > 0 {
			continue
		}
		var (
			status       string
			errorCode    string
			errorMessage string
		)
		err := s.db.Pool.QueryRow(ctx,
			`select status, error_code, error_message from ai_tasks where id = $1::uuid`, row.TaskID).
			Scan(&status, &errorCode, &errorMessage)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read ai task %s: %w", row.TaskID, err)
		}
		mapped, ok := mapTaskStatus(status)
		if !ok {
			continue
		}
		var feedback []byte
		if mapped == "completed" && len(row.Feedback) == 0 {
			feedback = s.loadReviewFeedback(ctx, row.TaskID)
		}
		if mapped == "failed" && len(row.Feedback) == 0 && (errorCode != "" || errorMessage != "") {
			feedback, _ = json.Marshal(map[string]any{
				"error": map[string]string{"code": errorCode, "message": errorMessage},
			})
		}
		if _, err := s.db.Pool.Exec(ctx, `
			update writing_reviews
			set status = $2,
			    feedback = coalesce($3::jsonb, feedback),
			    completed_at = case when $2 in ('completed', 'failed', 'cancelled') and completed_at is null
			                        then now() else completed_at end
			where id = $1::uuid`, row.ID, mapped, feedback); err != nil {
			return fmt.Errorf("update writing review %s: %w", row.ID, err)
		}
		row.Status = mapped
		if len(feedback) > 0 {
			row.Feedback = feedback
		}
		if isTerminalReviewStatus(mapped) && row.CompletedAt == nil {
			now := time.Now().UTC()
			row.CompletedAt = &now
		}
	}
	return nil
}

// loadReviewFeedback prefers an explicit feedback payload on the terminal
// event, then falls back to the last assistant message of the task.
func (s *Service) loadReviewFeedback(ctx context.Context, taskID string) []byte {
	var payload []byte
	if err := s.db.Pool.QueryRow(ctx,
		`select payload from ai_events where task_id = $1::uuid and type = 'done' order by seq desc limit 1`,
		taskID).Scan(&payload); err == nil {
		var event map[string]any
		if json.Unmarshal(payload, &event) == nil {
			if feedback, ok := event["feedback"]; ok {
				if encoded, err := json.Marshal(feedback); err == nil {
					return encoded
				}
			}
			if text, ok := event["text"].(string); ok && text != "" {
				if encoded, err := json.Marshal(map[string]string{"text": text}); err == nil {
					return encoded
				}
			}
		}
	}
	var content string
	if err := s.db.Pool.QueryRow(ctx, `
		select content from ai_messages
		where task_id = $1::uuid and role = 'assistant'
		order by created_at desc, id desc limit 1`, taskID).Scan(&content); err == nil && content != "" {
		if encoded, err := json.Marshal(map[string]string{"text": content}); err == nil {
			return encoded
		}
	}
	return nil
}

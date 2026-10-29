package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"rka/internal/httpx"
)

type taskItem struct {
	ID            string     `json:"id"`
	Source        string     `json:"source"` // ai_task | job
	Kind          string     `json:"kind"`
	Status        string     `json:"status"`
	UserEmail     string     `json:"userEmail,omitempty"`
	ErrorCode     string     `json:"errorCode,omitempty"`
	ErrorMessage  string     `json:"errorMessage,omitempty"`
	Errors        []any      `json:"errors,omitempty"`
	Attempts      int        `json:"attempts"`
	RetryCount    int        `json:"retryCount"`
	Retries       int        `json:"retries"`
	FailureReason string     `json:"failureReason,omitempty"`
	DurationMS    int64      `json:"durationMs"`
	CreatedAt     time.Time  `json:"createdAt"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
}

// handleTasks merges AI teaching tasks and content/backup jobs into one
// operator view with failure reasons, duration and retry counts.
func (s *Service) handleTasks(w http.ResponseWriter, r *http.Request) {
	limit, offset := paginationParams(r, 50, 200)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))

	aiItems, err := s.queryAITasks(r.Context(), status, kind, offset+limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	jobItems, err := s.queryJobs(r.Context(), status, kind, offset+limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := append(aiItems, jobItems...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].CreatedAt.After(items[j].CreatedAt) })
	if offset > len(items) {
		offset = len(items)
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	window := items[offset:end]
	nextCursor := ""
	if end < len(items) {
		nextCursor = strconv.Itoa(end)
	}
	if window == nil {
		window = []taskItem{}
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"items": window, "nextCursor": nextCursor})
}

func (s *Service) queryAITasks(ctx context.Context, status, kind string, fetch int) ([]taskItem, error) {
	rows, err := s.db.Pool.Query(ctx, `
		select t.id::text, t.kind, t.status, t.error_code, t.error_message, t.attempt,
		       t.created_at, t.started_at, t.finished_at, coalesce(u.email, '')
		from ai_tasks t left join users u on u.id = t.user_id
		where ($1 = '' or t.status = $1) and ($2 = '' or t.kind = $2)
		order by t.created_at desc limit $3`, status, kind, fetch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]taskItem, 0, fetch)
	now := time.Now()
	for rows.Next() {
		var item taskItem
		var attempt int
		if err := rows.Scan(&item.ID, &item.Kind, &item.Status, &item.ErrorCode, &item.ErrorMessage,
			&attempt, &item.CreatedAt, &item.StartedAt, &item.FinishedAt, &item.UserEmail); err != nil {
			return nil, err
		}
		item.Source = "ai_task"
		item.Attempts = attempt
		if attempt > 1 {
			item.RetryCount = attempt - 1
		}
		item.Retries = item.RetryCount
		item.FailureReason = item.ErrorMessage
		item.DurationMS = durationMS(item.StartedAt, item.FinishedAt, now)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Service) queryJobs(ctx context.Context, status, kind string, fetch int) ([]taskItem, error) {
	rows, err := s.db.Pool.Query(ctx, `
		select j.id::text, j.kind, j.status, j.errors, j.created_at, j.started_at, j.finished_at,
		       coalesce(u.email, '')
		from jobs j left join users u on u.id = j.created_by
		where ($1 = '' or j.status = $1) and ($2 = '' or j.kind = $2)
		order by j.created_at desc limit $3`, status, kind, fetch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]taskItem, 0, fetch)
	now := time.Now()
	for rows.Next() {
		var item taskItem
		var errorsRaw []byte
		if err := rows.Scan(&item.ID, &item.Kind, &item.Status, &errorsRaw,
			&item.CreatedAt, &item.StartedAt, &item.FinishedAt, &item.UserEmail); err != nil {
			return nil, err
		}
		item.Source = "job"
		if len(errorsRaw) > 0 {
			_ = json.Unmarshal(errorsRaw, &item.Errors)
			if len(item.Errors) > 0 {
				encoded, _ := json.Marshal(item.Errors[0])
				item.ErrorMessage = string(encoded)
			}
		}
		if item.StartedAt != nil {
			item.Attempts = 1
		}
		item.FailureReason = item.ErrorMessage
		item.Retries = item.RetryCount
		item.DurationMS = durationMS(item.StartedAt, item.FinishedAt, now)
		items = append(items, item)
	}
	return items, rows.Err()
}

func durationMS(startedAt, finishedAt *time.Time, now time.Time) int64 {
	if startedAt == nil {
		return 0
	}
	end := now
	if finishedAt != nil {
		end = *finishedAt
	}
	ms := end.Sub(*startedAt).Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms
}

func paginationParams(r *http.Request, defaultLimit, maxLimit int) (int, int) {
	limit := defaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	offset := 0
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 && parsed <= 1_000_000 {
			offset = parsed
		}
	}
	return limit, offset
}

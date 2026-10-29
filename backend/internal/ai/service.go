package ai

import (
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"rka/internal/auth"
	"rka/internal/config"
	"rka/internal/httpx"
	"rka/internal/store"
	"rka/internal/teaching"
)

// Deps wires the AI task service. Teaching is the execution adapter; DB holds
// the durable task queue, events and messages.
type Deps struct {
	Config   *config.Config
	DB       *store.DB
	Log      *slog.Logger
	Auth     *auth.Service
	Teaching *teaching.Service
}

type Service struct {
	cfg      *config.Config
	db       *store.DB
	log      *slog.Logger
	auth     *auth.Service
	teaching *teaching.Service
}

func New(deps Deps) *Service {
	return &Service{cfg: deps.Config, db: deps.DB, log: deps.Log, auth: deps.Auth, teaching: deps.Teaching}
}

// Teaching exposes the execution adapter to the worker.
func (s *Service) Teaching() *teaching.Service { return s.teaching }

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/ai/status", s.auth.Require(s.handleStatus))
	mux.HandleFunc("POST /api/ai/tasks", s.auth.Require(s.handleCreateTask))
	mux.HandleFunc("GET /api/ai/tasks", s.auth.Require(s.handleListTasks))
	mux.HandleFunc("GET /api/ai/tasks/{id}", s.auth.Require(s.handleGetTask))
	mux.HandleFunc("DELETE /api/ai/tasks/{id}", s.auth.Require(s.handleDeleteTask))
	mux.HandleFunc("GET /api/ai/tasks/{id}/events", s.auth.Require(s.handleEvents))
	mux.HandleFunc("POST /api/ai/tasks/{id}/messages", s.auth.Require(s.handleMessage))
	mux.HandleFunc("POST /api/ai/tasks/{id}/cancel", s.auth.Require(s.handleCancel))
}

// CreateTaskInput is the contract every caller (writing, practice, admin) uses
// to enqueue a teaching task. Implementations must be idempotent on
// (UserID, IdempotencyKey).
type CreateTaskInput struct {
	UserID         string         `json:"userId"`
	Kind           string         `json:"kind"`
	IdempotencyKey string         `json:"idempotencyKey"`
	Context        map[string]any `json:"context"`
}

// Task is the browser-facing task projection.
type Task struct {
	ID              string         `json:"id"`
	Kind            string         `json:"kind"`
	Status          string         `json:"status"`
	Context         map[string]any `json:"context"`
	ErrorCode       string         `json:"errorCode,omitempty"`
	ErrorText       string         `json:"errorMessage,omitempty"`
	CreatedAt       time.Time      `json:"createdAt"`
	StartedAt       *time.Time     `json:"startedAt,omitempty"`
	FinishedAt      *time.Time     `json:"finishedAt,omitempty"`
	Attempts        int            `json:"attempts"`
	CancelRequested bool           `json:"cancelRequested"`
}

// TaskFilter selects tasks for the admin and "my tasks" views.
type TaskFilter struct {
	UserID string
	Status string
	Kind   string
	Limit  int
	Cursor string
}

// TaskSummary is the admin projection with failure reasons and timings.
type TaskSummary struct {
	Task
	UserEmail  string `json:"userEmail"`
	DurationMS int64  `json:"durationMs"`
}

// ---------------------------------------------------------------- handlers

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	report, err := s.Status(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, report)
}

type createTaskRequest struct {
	Kind           string         `json:"kind"`
	IdempotencyKey string         `json:"idempotencyKey"`
	Context        map[string]any `json:"context"`
}

func (s *Service) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	var request createTaskRequest
	if err := httpx.Decode(w, r, &request, 1<<20); err != nil {
		httpx.Error(w, r, err)
		return
	}
	user := auth.MustUser(r.Context())
	task, created, err := s.createTask(r.Context(), CreateTaskInput{
		UserID:         user.ID,
		Kind:           request.Kind,
		IdempotencyKey: request.IdempotencyKey,
		Context:        request.Context,
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, r, status, map[string]any{"task": task})
}

func (s *Service) handleGetTask(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	task, err := s.TaskForUser(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, mapTaskError(err))
		return
	}
	events, err := s.RecentEvents(r.Context(), task.ID, 100)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"task": task, "events": events})
}

func (s *Service) handleListTasks(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	items, err := s.ListTasks(r.Context(), TaskFilter{
		UserID: user.ID,
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Kind:   strings.TrimSpace(r.URL.Query().Get("kind")),
		Limit:  limit,
		Cursor: strings.TrimSpace(r.URL.Query().Get("cursor")),
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	nextCursor := ""
	if len(items) == limit {
		last := items[len(items)-1]
		nextCursor = last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"items": items, "nextCursor": nextCursor})
}

func (s *Service) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	if err := s.DeleteTask(r.Context(), user.ID, r.PathValue("id")); err != nil {
		httpx.Error(w, r, mapTaskError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type messageRequest struct {
	Content string `json:"content"`
	Message string `json:"message"`
	Text    string `json:"text"`
}

func (s *Service) handleMessage(w http.ResponseWriter, r *http.Request) {
	var request messageRequest
	if err := httpx.Decode(w, r, &request, 256<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	content := firstNonEmptyString(request.Content, request.Message, request.Text)
	user := auth.MustUser(r.Context())
	task, err := s.PostMessage(r.Context(), user.ID, r.PathValue("id"), content)
	if err != nil {
		httpx.Error(w, r, mapTaskError(err))
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"task": task})
}

func (s *Service) handleCancel(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	task, err := s.RequestCancel(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, mapTaskError(err))
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"task": task})
}

// mapTaskError keeps ownership failures indistinguishable from missing tasks so
// one user cannot probe another user's task ids.
func mapTaskError(err error) error {
	if err == nil {
		return nil
	}
	if err == store.ErrNotFound {
		return httpx.ErrNotFound
	}
	return err
}

func sanitizeEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

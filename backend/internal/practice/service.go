// Package practice owns practice sessions, answer facts, scoring, formal exams
// and question marks.
//
// The server is the authority: it chooses the question queue and pins one
// question revision per queue entry when a session is created, records every
// answer as an immutable attempt keyed by the client event id, and derives the
// wrong-book, review schedule and statistics from those facts.
package practice

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"rka/internal/auth"
	"rka/internal/config"
	"rka/internal/content"
	"rka/internal/httpx"
	"rka/internal/store"
)

// Deps is the wiring contract for the package. Keep this struct stable: the
// application assembly and integration tests both construct services from it.
type Deps struct {
	Config  *config.Config
	DB      *store.DB
	Log     *slog.Logger
	Auth    *auth.Service
	Content *content.Service
}

type Service struct {
	cfg     *config.Config
	db      *store.DB
	log     *slog.Logger
	auth    *auth.Service
	content *content.Service
}

func New(deps Deps) *Service {
	return &Service{cfg: deps.Config, db: deps.DB, log: deps.Log, auth: deps.Auth, content: deps.Content}
}

// Register installs the HTTP routes owned by this package.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/practice/sessions", s.auth.Require(s.handleCreateSession))
	mux.HandleFunc("GET /api/practice/sessions/active", s.auth.Require(s.handleActiveSession))
	mux.HandleFunc("GET /api/practice/sessions/{id}", s.auth.Require(s.handleGetSession))
	mux.HandleFunc("PATCH /api/practice/sessions/{id}", s.auth.Require(s.handlePatchSession))
	mux.HandleFunc("POST /api/practice/sessions/{id}/answers", s.auth.Require(s.handleAnswer))
	mux.HandleFunc("POST /api/practice/sessions/{id}/submit", s.auth.Require(s.handleSubmit))
	mux.HandleFunc("POST /api/practice/sessions/{id}/finish", s.auth.Require(s.handleFinish))

	mux.HandleFunc("GET /api/practice/attempts", s.auth.Require(s.handleAttempts))
	mux.HandleFunc("GET /api/practice/wrong-book", s.auth.Require(s.handleWrongBook))
	mux.HandleFunc("GET /api/practice/review-due", s.auth.Require(s.handleReviewDue))
	mux.HandleFunc("GET /api/practice/marks", s.auth.Require(s.handleListMarks))
	mux.HandleFunc("PUT /api/practice/marks/{questionId}", s.auth.Require(s.handlePutMark))
	mux.HandleFunc("GET /api/practice/stats", s.auth.Require(s.handleStats))
}

const (
	defaultPracticeCount = 20
	maxPracticeCount     = 200
	defaultExamMinutes   = 150
	maxExamMinutes       = 24 * 60
	maxRequestBody       = 1 << 20
	defaultAttemptsLimit = 50
	maxListLimit         = 200
	defaultMarksLimit    = 100
	maxNoteLength        = 10_000
	defaultRuleJSON      = `{"type":"single-choice","points":1,"ruleVersion":"v1"}`
)

// Domain errors. Messages are user facing (Chinese); codes are stable for the
// frontend and tests.
var (
	errActiveSession      = httpx.NewError(http.StatusConflict, "active_session", "已有进行中的练习，请先结束当前练习，或使用 replace 重新开始。")
	errDeadlinePassed     = httpx.NewError(http.StatusConflict, "deadline_passed", "考试已超过截止时间，已按已收到的作答记录交卷。")
	errSessionFinished    = httpx.NewError(http.StatusConflict, "session_finished", "该练习现场已结束。")
	errNoQuestions        = httpx.NewError(http.StatusBadRequest, "no_questions", "当前范围没有可练题目，请调整筛选。")
	errExamRequiresSubmit = httpx.NewError(http.StatusBadRequest, "exam_requires_submit", "正式考试请使用交卷接口。")
)

func badRequest(code, message string) *httpx.APIError {
	return httpx.NewError(http.StatusBadRequest, code, message)
}

// querier is the subset of pgx shared by the pool and a transaction.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// scope is the normalised practice range. Empty strings mean "no filter";
// "all"/"any" from the browser are normalised to empty.
type scope struct {
	MaterialID     string `json:"materialId,omitempty"`
	SectionID      string `json:"sectionId,omitempty"`
	Module         string `json:"module,omitempty"`
	Term           string `json:"term,omitempty"`
	Source         string `json:"source,omitempty"`
	Query          string `json:"query,omitempty"`
	IncludeMissing bool   `json:"includeMissing,omitempty"`
}

type createSessionRequest struct {
	Mode            string `json:"mode"`
	Scope           scope  `json:"scope"`
	Count           int    `json:"count"`
	Replace         bool   `json:"replace"`
	DurationMinutes int    `json:"durationMinutes"`
}

func validMode(mode string) bool {
	switch mode {
	case "chapter", "all", "exam", "wrong", "favorites", "review":
		return true
	}
	return false
}

func normaliseFilter(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case "", "all", "any":
		return ""
	}
	return value
}

func normaliseScope(in scope) scope {
	return scope{
		MaterialID:     strings.TrimSpace(in.MaterialID),
		SectionID:      strings.TrimSpace(in.SectionID),
		Module:         normaliseFilter(in.Module),
		Term:           normaliseFilter(in.Term),
		Source:         normaliseFilter(in.Source),
		Query:          strings.TrimSpace(in.Query),
		IncludeMissing: in.IncludeMissing,
	}
}

func validateScope(mode string, sc scope) error {
	if sc.Source != "" {
		switch sc.Source {
		case "real", "mock", "ai":
		default:
			return badRequest("invalid_source", "题目来源不正确。")
		}
	}
	if sc.SectionID != "" && sc.MaterialID == "" {
		return badRequest("section_requires_material", "指定小节时必须同时指定资料。")
	}
	if mode == "chapter" && sc.MaterialID == "" {
		return badRequest("material_required", "章节练习需要指定资料。")
	}
	if mode == "exam" && sc.Term == "" {
		return badRequest("term_required", "正式考试需要选择一套试卷。")
	}
	// A timed exam is drawn from real / recalled papers only; AI-generated
	// practice questions must never appear in one.
	if mode == "exam" && sc.Source != "" && sc.Source != "real" {
		return badRequest("invalid_source", "正式考试只能使用真题或回忆版试卷。")
	}
	return nil
}

// sameRange reports whether two requests address the same practice range. The
// count is a sizing preference, not part of the range identity: reopening the
// same chapter continues the original queue.
func sameRange(modeA string, a scope, modeB string, b scope) bool {
	return modeA == modeB &&
		a.MaterialID == b.MaterialID &&
		a.SectionID == b.SectionID &&
		a.Module == b.Module &&
		a.Term == b.Term &&
		a.Source == b.Source &&
		a.Query == b.Query &&
		a.IncludeMissing == b.IncludeMissing
}

// decodeBody reads a bounded JSON body. An empty body leaves dst untouched,
// which lets POST /submit and /finish be called without a body.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return httpx.NewError(http.StatusRequestEntityTooLarge, "too_large", "请求内容过大。")
		}
		return badRequest("bad_request", "请求内容读取失败。")
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return badRequest("bad_request", "请求格式不正确："+err.Error())
	}
	if decoder.More() {
		return badRequest("bad_request", "请求体只能包含一个 JSON 对象。")
	}
	return nil
}

func nowInTx(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `select now()`).Scan(&now); err != nil {
		return time.Time{}, fmt.Errorf("server time: %w", err)
	}
	return now.UTC(), nil
}

// bankVersion reports the published content version recorded by the importer.
// It reads platform_state directly so practice does not depend on the content
// package being fully wired.
func (s *Service) bankVersion(ctx context.Context, q querier) (string, error) {
	var version string
	err := q.QueryRow(ctx, `
		select coalesce(nullif(bank_version, ''), nullif(content_version, ''), '')
		from platform_state where id = 1`).Scan(&version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("bank version: %w", err)
	}
	return version, nil
}

// pageParams implements the shared ?limit=&cursor= convention. The cursor is an
// opaque offset so list endpoints behave identically for the frontend.
type pageParams struct {
	Limit  int
	Offset int
}

func parsePage(r *http.Request, defaultLimit, maxLimit int) (pageParams, error) {
	params := pageParams{Limit: defaultLimit}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return params, badRequest("invalid_limit", "分页数量不正确。")
		}
		if value > maxLimit {
			value = maxLimit
		}
		params.Limit = value
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			return params, badRequest("invalid_cursor", "分页游标不正确。")
		}
		offset, err := strconv.Atoi(string(decoded))
		if err != nil || offset < 0 {
			return params, badRequest("invalid_cursor", "分页游标不正确。")
		}
		params.Offset = offset
	}
	return params, nil
}

func encodeCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

// mapLoadError converts storage misses (and malformed uuid path values) into
// the public 404 envelope instead of a 500.
func mapLoadError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return httpx.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
		return httpx.ErrNotFound
	}
	return err
}

func utcPtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	converted := value.UTC()
	return &converted
}

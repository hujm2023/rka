package content

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"rka/internal/auth"
	"rka/internal/httpx"
)

// handleListQuestions serves the paginated public question index. The query
// never touches answer/analysis columns.
func (s *Service) handleListQuestions(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit := intQuery(query.Get("limit"), 20, 1, 200)

	var (
		args       []any
		conditions []string
	)
	add := func(condition string, value any) {
		args = append(args, value)
		conditions = append(conditions, fmt.Sprintf(condition, len(args)))
	}
	if value := strings.TrimSpace(query.Get("module")); value != "" {
		add("q.module = $%d", value)
	}
	if value := strings.TrimSpace(query.Get("term")); value != "" {
		add("q.term = $%d", value)
	}
	if value := strings.TrimSpace(query.Get("sourceType")); value != "" {
		add("q.source_type = $%d", value)
	}
	if value := strings.TrimSpace(query.Get("reviewStatus")); value != "" {
		add("q.review_status = $%d", value)
	}
	if value := strings.TrimSpace(query.Get("knowledge")); value != "" {
		add("q.knowledge ilike $%d", "%"+value+"%")
	}
	if value := strings.TrimSpace(query.Get("figureMissing")); value == "true" || value == "false" {
		add("q.figure_missing = $%d", value == "true")
	}
	if value := strings.TrimSpace(query.Get("q")); value != "" {
		args = append(args, "%"+value+"%")
		index := len(args)
		conditions = append(conditions, fmt.Sprintf(
			"(coalesce(v.stem, '') ilike $%d or q.knowledge ilike $%d or q.term ilike $%d)", index, index, index))
	}
	if cursor := strings.TrimSpace(query.Get("cursor")); cursor != "" {
		id, err := decodeCursor(cursor)
		if err != nil {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_cursor", "分页游标不正确。"))
			return
		}
		add("q.id > $%d", id)
	}
	where := ""
	if len(conditions) > 0 {
		where = joinConditions(conditions)
	}
	views, err := s.loadQuestionViews(r.Context(), where, args, "q.id", limit+1)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	nextCursor := ""
	if len(views) > limit {
		views = views[:limit]
		nextCursor = encodeCursor(views[len(views)-1].ID)
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"items": views, "nextCursor": nextCursor})
}

func (s *Service) handleGetQuestion(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	views, err := s.loadQuestionViews(r.Context(), "q.id = $1", []any{id}, "", 1)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if len(views) == 0 {
		httpx.Error(w, r, httpx.ErrNotFound)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"question": views[0]})
}

// handleQuestionSolution reveals the answer and analysis only to administrators
// or to a learner who has already submitted an attempt, and never while an exam
// containing the question is still active.
func (s *Service) handleQuestionSolution(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	user := auth.MustUser(ctx)

	solution, err := s.loadQuestionSolution(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, r, httpx.ErrNotFound)
			return
		}
		httpx.Error(w, r, err)
		return
	}
	if !user.IsAdmin() {
		hidden, err := s.examAnswerHidden(ctx, user.ID, id)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		if hidden {
			httpx.JSON(w, r, http.StatusOK, map[string]any{
				"questionId":   id,
				"revision":     solution.Revision,
				"answerHidden": true,
			})
			return
		}
		attempted, err := s.hasAttempted(ctx, user.ID, id)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		if !attempted {
			httpx.Error(w, r, httpx.NewError(http.StatusForbidden, "solution_locked",
				"提交作答后才能查看答案与解析。"))
			return
		}
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"questionId":   solution.QuestionID,
		"revision":     solution.Revision,
		"answer":       solution.Answer,
		"analysis":     solution.Analysis,
		"scoringRule":  solution.ScoringRule,
		"answerHidden": false,
	})
}

// examAnswerHidden reports whether an active, unsubmitted exam contains the
// question. Answer display is blocked for the whole exam, not only for
// questions already answered.
func (s *Service) examAnswerHidden(ctx context.Context, userID, questionID string) (bool, error) {
	var hidden bool
	err := s.db.Pool.QueryRow(ctx, `
		select exists (
			select 1 from practice_sessions
			where user_id = $1::uuid and mode = 'exam' and status = 'active'
			  and submitted_at is null and $2 = any(question_ids)
		)`, userID, questionID).Scan(&hidden)
	if err != nil {
		return false, fmt.Errorf("query active exam: %w", err)
	}
	return hidden, nil
}

func (s *Service) hasAttempted(ctx context.Context, userID, questionID string) (bool, error) {
	var attempted bool
	err := s.db.Pool.QueryRow(ctx,
		`select exists (select 1 from released_attempts where user_id = $1::uuid and question_id = $2)`,
		userID, questionID).Scan(&attempted)
	if err != nil {
		return false, fmt.Errorf("query attempts: %w", err)
	}
	return attempted, nil
}

func encodeCursor(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id))
}

func decodeCursor(cursor string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil || len(raw) == 0 {
		return "", fmt.Errorf("invalid cursor")
	}
	return string(raw), nil
}

func intQuery(raw string, fallback, min, max int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

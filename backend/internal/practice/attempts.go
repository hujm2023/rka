package practice

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"rka/internal/auth"
	"rka/internal/httpx"
	"rka/internal/learning"
)

type listResponse struct {
	Items      any    `json:"items"`
	NextCursor string `json:"nextCursor"`
}

// ---------------------------------------------------------------- attempts

func (s *Service) handleAttempts(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	params, err := parsePage(r, defaultAttemptsLimit, maxListLimit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	args := []any{user.ID}
	query := `
		select id, question_id, question_revision, selected, correct, assisted, duration_ms,
		       answered_at, session_id::text, created_at
		from released_attempts where user_id = $1::uuid`
	if questionID := strings.TrimSpace(r.URL.Query().Get("questionId")); questionID != "" {
		args = append(args, questionID)
		query += fmt.Sprintf(" and question_id = $%d", len(args))
	}
	args = append(args, params.Limit+1, params.Offset)
	query += fmt.Sprintf(" order by answered_at desc, created_at desc, id desc limit $%d offset $%d", len(args)-1, len(args))
	rows, err := s.db.Pool.Query(r.Context(), query, args...)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	defer rows.Close()
	items := []attemptView{}
	for rows.Next() {
		var attempt attemptRow
		if err := rows.Scan(&attempt.ID, &attempt.QuestionID, &attempt.Revision, &attempt.Selected,
			&attempt.Correct, &attempt.Assisted, &attempt.DurationMS, &attempt.AnsweredAt,
			&attempt.SessionID, &attempt.CreatedAt); err != nil {
			httpx.Error(w, r, err)
			return
		}
		items = append(items, attempt.view())
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

// ---------------------------------------------------------------- wrong book

type wrongBookItem struct {
	ID          string          `json:"id"`
	QuestionID  string          `json:"questionId"`
	Revision    string          `json:"revision"`
	Question    questionSummary `json:"question"`
	LastAttempt attemptView     `json:"lastAttempt"`
	WrongCount  int             `json:"wrongCount"`
	LastWrongAt *time.Time      `json:"lastWrongAt"`
}

func (s *Service) handleWrongBook(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	params, err := parsePage(r, defaultAttemptsLimit, maxListLimit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	args := []any{user.ID}
	query := `
		select distinct on (a.question_id)
		       a.id, a.question_id, a.question_revision, a.selected, a.correct, a.assisted,
		       a.duration_ms, a.answered_at, a.session_id::text, a.created_at,
		       q.module, q.term, q.paper, q.question_no, q.knowledge, q.difficulty,
		       q.source_type, q.figure_missing, q.multi_blank, v.stem, v.options,
		       w.wrong_count, w.last_wrong_at
		from released_attempts a
		join questions q on q.id = a.question_id
		join question_versions v on v.question_id = a.question_id and v.revision = a.question_revision
		join lateral (
			select (count(*) filter (where not x.correct))::int as wrong_count,
			       max(x.answered_at) filter (where not x.correct) as last_wrong_at
			from released_attempts x
			where x.user_id = a.user_id and x.question_id = a.question_id
		) w on true
		where a.user_id = $1::uuid`
	if module := strings.TrimSpace(r.URL.Query().Get("module")); module != "" && module != "all" {
		args = append(args, module)
		query += fmt.Sprintf(" and q.module = $%d", len(args))
	}
	if term := strings.TrimSpace(r.URL.Query().Get("term")); term != "" && term != "all" {
		args = append(args, term)
		query += fmt.Sprintf(" and q.term = $%d", len(args))
	}
	query += " order by a.question_id, a.answered_at desc, a.created_at desc"
	rows, err := s.db.Pool.Query(r.Context(), query, args...)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	defer rows.Close()

	var items []wrongBookItem
	for rows.Next() {
		var (
			attempt attemptRow
			summary questionSummary
			wrong   int
			lastAt  *time.Time
		)
		if err := rows.Scan(&attempt.ID, &attempt.QuestionID, &attempt.Revision, &attempt.Selected,
			&attempt.Correct, &attempt.Assisted, &attempt.DurationMS, &attempt.AnsweredAt,
			&attempt.SessionID, &attempt.CreatedAt,
			&summary.Module, &summary.Term, &summary.Paper, &summary.QuestionNo, &summary.Knowledge,
			&summary.Difficulty, &summary.SourceType, &summary.FigureMissing, &summary.MultiBlank,
			&summary.Stem, &summary.Options, &wrong, &lastAt); err != nil {
			httpx.Error(w, r, err)
			return
		}
		if attempt.Correct {
			continue // Only questions whose latest attempt is still wrong.
		}
		summary.ID = attempt.QuestionID
		summary.Revision = attempt.Revision
		if summary.Options == nil {
			summary.Options = json.RawMessage(`{}`)
		}
		items = append(items, wrongBookItem{
			ID:          attempt.QuestionID,
			QuestionID:  attempt.QuestionID,
			Revision:    attempt.Revision,
			Question:    summary,
			LastAttempt: attempt.view(),
			WrongCount:  wrong,
			LastWrongAt: utcPtr(lastAt),
		})
	}
	if err := rows.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		leftAt := left.LastAttempt.AnsweredAt
		if left.LastWrongAt != nil {
			leftAt = *left.LastWrongAt
		}
		rightAt := right.LastAttempt.AnsweredAt
		if right.LastWrongAt != nil {
			rightAt = *right.LastWrongAt
		}
		if !leftAt.Equal(rightAt) {
			return leftAt.After(rightAt)
		}
		return left.QuestionID < right.QuestionID
	})
	next := ""
	if params.Offset >= len(items) {
		items = []wrongBookItem{}
	} else {
		items = items[params.Offset:]
	}
	if len(items) > params.Limit {
		items = items[:params.Limit]
		next = encodeCursor(params.Offset + params.Limit)
	}
	if items == nil {
		items = []wrongBookItem{}
	}
	httpx.JSON(w, r, http.StatusOK, listResponse{Items: items, NextCursor: next})
}

// ---------------------------------------------------------------- review due

type reviewDueItem struct {
	ID           string          `json:"id"`
	QuestionID   string          `json:"questionId"`
	Revision     string          `json:"revision"`
	DueAt        time.Time       `json:"dueAt"`
	IntervalDays int             `json:"intervalDays"`
	RuleVersion  string          `json:"ruleVersion"`
	LastAttempt  attemptView     `json:"lastAttempt"`
	Question     questionSummary `json:"question"`
}

// latestAttempts returns the most recent attempt per question for a learner.
func (s *Service) latestAttempts(ctx context.Context, userID string) ([]attemptRow, error) {
	rows, err := s.db.Pool.Query(ctx, `
		select distinct on (question_id) id, question_id, question_revision, selected, correct,
		       assisted, duration_ms, answered_at, session_id::text, created_at
		from released_attempts
		where user_id = $1::uuid
		order by question_id, answered_at desc, created_at desc`, userID)
	if err != nil {
		return nil, fmt.Errorf("load latest attempts: %w", err)
	}
	defer rows.Close()
	var items []attemptRow
	for rows.Next() {
		var attempt attemptRow
		if err := rows.Scan(&attempt.ID, &attempt.QuestionID, &attempt.Revision, &attempt.Selected,
			&attempt.Correct, &attempt.Assisted, &attempt.DurationMS, &attempt.AnsweredAt,
			&attempt.SessionID, &attempt.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, attempt)
	}
	return items, rows.Err()
}

func (s *Service) handleReviewDue(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	params, err := parsePage(r, defaultAttemptsLimit, maxListLimit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	now, err := s.db.Now(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	latest, err := s.latestAttempts(r.Context(), user.ID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	type dueEntry struct {
		attempt attemptRow
		dueAt   time.Time
	}
	var due []dueEntry
	for _, attempt := range latest {
		dueAt := learning.ReviewDueAt(attempt.AnsweredAt, attempt.Correct, attempt.Assisted)
		if dueAt.After(now) {
			continue
		}
		due = append(due, dueEntry{attempt: attempt, dueAt: dueAt})
	}
	sort.SliceStable(due, func(i, j int) bool {
		if !due[i].dueAt.Equal(due[j].dueAt) {
			return due[i].dueAt.Before(due[j].dueAt)
		}
		return due[i].attempt.QuestionID < due[j].attempt.QuestionID
	})
	next := ""
	page := due
	if params.Offset >= len(page) {
		page = nil
	} else {
		page = page[params.Offset:]
	}
	if len(page) > params.Limit {
		page = page[:params.Limit]
		next = encodeCursor(params.Offset + params.Limit)
	}
	items := []reviewDueItem{}
	if len(page) > 0 {
		questionIDs := make([]string, 0, len(page))
		revisions := map[string]string{}
		for _, entry := range page {
			questionIDs = append(questionIDs, entry.attempt.QuestionID)
			revisions[entry.attempt.QuestionID] = entry.attempt.Revision
		}
		versions, err := s.questionVersions(r.Context(), s.db.Pool, questionIDs)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		for _, entry := range page {
			version := versions[entry.attempt.QuestionID+"\x00"+entry.attempt.Revision]
			summary := questionSummary{
				ID:            entry.attempt.QuestionID,
				Revision:      entry.attempt.Revision,
				Module:        version.Module,
				Term:          version.Term,
				Paper:         version.Paper,
				QuestionNo:    version.QuestionNo,
				Knowledge:     version.Knowledge,
				Difficulty:    version.Difficulty,
				SourceType:    version.SourceType,
				Stem:          version.Stem,
				Options:       version.Options,
				FigureMissing: version.FigureMissing,
				MultiBlank:    version.MultiBlank,
			}
			if summary.Options == nil {
				summary.Options = json.RawMessage(`{}`)
			}
			items = append(items, reviewDueItem{
				ID:           entry.attempt.QuestionID,
				QuestionID:   entry.attempt.QuestionID,
				Revision:     entry.attempt.Revision,
				DueAt:        entry.dueAt.UTC(),
				IntervalDays: learning.ReviewIntervalDays(entry.attempt.Correct, entry.attempt.Assisted),
				RuleVersion:  learning.RuleVersion,
				LastAttempt:  entry.attempt.view(),
				Question:     summary,
			})
		}
	}
	httpx.JSON(w, r, http.StatusOK, listResponse{Items: items, NextCursor: next})
}

// ---------------------------------------------------------------- stats

type statsTotals struct {
	Attempts           int     `json:"attempts"`
	Correct            int     `json:"correct"`
	Accuracy           float64 `json:"accuracy"`
	AccuracyPercent    int     `json:"accuracyPercent"`
	IndependentCorrect int     `json:"independentCorrect"`
	AssistedCorrect    int     `json:"assistedCorrect"`
	DistinctQuestions  int     `json:"distinctQuestions"`
	WrongQuestions     int     `json:"wrongQuestions"`
	DueQuestions       int     `json:"dueQuestions"`
	FavoriteCount      int     `json:"favoriteCount"`
	UncertainCount     int     `json:"uncertainCount"`
	ActiveSessions     int     `json:"activeSessions"`
}

type moduleStats struct {
	Module             string     `json:"module"`
	TotalQuestions     int        `json:"totalQuestions"`
	AttemptedQuestions int        `json:"attemptedQuestions"`
	Progress           float64    `json:"progress"`
	ProgressPercent    int        `json:"progressPercent"`
	Attempts           int        `json:"attempts"`
	Correct            int        `json:"correct"`
	Accuracy           float64    `json:"accuracy"`
	AccuracyPercent    int        `json:"accuracyPercent"`
	LastAnsweredAt     *time.Time `json:"lastAnsweredAt"`
}

type recentActivityItem struct {
	AttemptID  string    `json:"attemptId"`
	QuestionID string    `json:"questionId"`
	Revision   string    `json:"revision"`
	Correct    bool      `json:"correct"`
	Assisted   bool      `json:"assisted"`
	AnsweredAt time.Time `json:"answeredAt"`
	Module     string    `json:"module"`
	Term       string    `json:"term"`
	Stem       string    `json:"stem"`
}

type dailyActivity struct {
	Date     string `json:"date"`
	Attempts int    `json:"attempts"`
	Correct  int    `json:"correct"`
}

func ratio(correct, total int) (float64, int) {
	if total == 0 {
		return 0, 0
	}
	value := float64(correct) / float64(total)
	return math.Round(value*10000) / 10000, int(math.Round(value * 100))
}

func (s *Service) handleStats(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	ctx := r.Context()
	now, err := s.db.Now(ctx)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var totals statsTotals
	var firstAnswered, lastAnswered *time.Time
	err = s.db.Pool.QueryRow(ctx, `
		select count(*), count(*) filter (where correct),
		       count(*) filter (where correct and not assisted),
		       count(*) filter (where correct and assisted),
		       count(distinct question_id), min(answered_at), max(answered_at)
		from released_attempts where user_id = $1::uuid`, user.ID).
		Scan(&totals.Attempts, &totals.Correct, &totals.IndependentCorrect, &totals.AssistedCorrect,
			&totals.DistinctQuestions, &firstAnswered, &lastAnswered)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	totals.Accuracy, totals.AccuracyPercent = ratio(totals.Correct, totals.Attempts)
	if err := s.db.Pool.QueryRow(ctx, `
		select (count(*) filter (where favorite))::int, (count(*) filter (where uncertain))::int
		from marks where user_id = $1::uuid`, user.ID).
		Scan(&totals.FavoriteCount, &totals.UncertainCount); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := s.db.Pool.QueryRow(ctx, `
		select count(*) from practice_sessions where user_id = $1::uuid and status = 'active'`,
		user.ID).Scan(&totals.ActiveSessions); err != nil {
		httpx.Error(w, r, err)
		return
	}
	latest, err := s.latestAttempts(ctx, user.ID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	for _, attempt := range latest {
		if !attempt.Correct {
			totals.WrongQuestions++
		}
		if learning.ReviewDueAt(attempt.AnsweredAt, attempt.Correct, attempt.Assisted).Before(now) ||
			learning.ReviewDueAt(attempt.AnsweredAt, attempt.Correct, attempt.Assisted).Equal(now) {
			totals.DueQuestions++
		}
	}

	modules := []moduleStats{}
	rows, err := s.db.Pool.Query(ctx, `
		select q.module, count(distinct q.id), count(distinct a.question_id),
		       count(a.id), count(a.id) filter (where a.correct), max(a.answered_at)
		from questions q
		left join released_attempts a on a.question_id = q.id and a.user_id = $1::uuid
		where q.review_status = 'published'
		group by q.module
		order by q.module`, user.ID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	for rows.Next() {
		var item moduleStats
		if err := rows.Scan(&item.Module, &item.TotalQuestions, &item.AttemptedQuestions,
			&item.Attempts, &item.Correct, &item.LastAnsweredAt); err != nil {
			rows.Close()
			httpx.Error(w, r, err)
			return
		}
		item.Accuracy, item.AccuracyPercent = ratio(item.Correct, item.Attempts)
		item.Progress, item.ProgressPercent = ratio(item.AttemptedQuestions, item.TotalQuestions)
		item.LastAnsweredAt = utcPtr(item.LastAnsweredAt)
		modules = append(modules, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}

	recent := []recentActivityItem{}
	rows, err = s.db.Pool.Query(ctx, `
		select a.id, a.question_id, a.question_revision, a.correct, a.assisted, a.answered_at,
		       q.module, q.term, coalesce(v.stem, '')
		from released_attempts a
		join questions q on q.id = a.question_id
		left join question_versions v on v.question_id = a.question_id and v.revision = a.question_revision
		where a.user_id = $1::uuid
		order by a.answered_at desc, a.created_at desc, a.id desc
		limit 10`, user.ID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	for rows.Next() {
		var item recentActivityItem
		if err := rows.Scan(&item.AttemptID, &item.QuestionID, &item.Revision, &item.Correct,
			&item.Assisted, &item.AnsweredAt, &item.Module, &item.Term, &item.Stem); err != nil {
			rows.Close()
			httpx.Error(w, r, err)
			return
		}
		item.AnsweredAt = item.AnsweredAt.UTC()
		recent = append(recent, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}

	activity := []dailyActivity{}
	rows, err = s.db.Pool.Query(ctx, `
		select ((answered_at at time zone 'UTC')::date)::text, count(*), count(*) filter (where correct)
		from released_attempts
		where user_id = $1::uuid and answered_at >= now() - interval '14 days'
		group by 1 order by 1`, user.ID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	for rows.Next() {
		var item dailyActivity
		if err := rows.Scan(&item.Date, &item.Attempts, &item.Correct); err != nil {
			rows.Close()
			httpx.Error(w, r, err)
			return
		}
		activity = append(activity, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"serverTime": now.UTC(),
		"totals":     totals,
		"modules":    modules,
		"recent":     recent,
		"activity":   activity,
		"range": map[string]any{
			"firstAnsweredAt": utcPtr(firstAnswered),
			"lastAnsweredAt":  utcPtr(lastAnswered),
		},
	})
}

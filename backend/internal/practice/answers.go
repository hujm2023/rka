package practice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"rka/internal/auth"
	"rka/internal/httpx"
	"rka/internal/learning"
	"rka/internal/store"
)

type answerRequest struct {
	ID         string `json:"id"`
	QuestionID string `json:"questionId"`
	Selected   string `json:"selected"`
	Assisted   bool   `json:"assisted"`
	DurationMS int    `json:"durationMs"`
	AnsweredAt string `json:"answeredAt"`
}

type attemptView struct {
	ID         string    `json:"id"`
	AttemptID  string    `json:"attemptId"`
	QuestionID string    `json:"questionId"`
	Revision   string    `json:"revision"`
	Selected   string    `json:"selected"`
	Correct    *bool     `json:"correct,omitempty"`
	Assisted   bool      `json:"assisted"`
	DurationMS int       `json:"durationMs"`
	AnsweredAt time.Time `json:"answeredAt"`
	SessionID  string    `json:"sessionId,omitempty"`
}

type attemptRow struct {
	ID         string
	UserID     string
	QuestionID string
	Revision   string
	Selected   string
	Correct    bool
	Assisted   bool
	DurationMS int
	AnsweredAt time.Time
	SessionID  *string
	CreatedAt  time.Time
}

func (a attemptRow) view() attemptView {
	correct := a.Correct
	view := attemptView{
		ID:         a.ID,
		AttemptID:  a.ID,
		QuestionID: a.QuestionID,
		Revision:   a.Revision,
		Selected:   a.Selected,
		Correct:    &correct,
		Assisted:   a.Assisted,
		DurationMS: a.DurationMS,
		AnsweredAt: a.AnsweredAt.UTC(),
	}
	if a.SessionID != nil {
		view.SessionID = *a.SessionID
	}
	return view
}

type questionVersion struct {
	QuestionID    string
	Revision      string
	Module        string
	Term          string
	Paper         string
	QuestionNo    int
	Knowledge     string
	Difficulty    string
	SourceType    string
	FigureMissing bool
	MultiBlank    bool
	Stem          string
	Options       []byte
	Answer        string
	Analysis      string
	ScoringRule   []byte
}

type solutionView struct {
	Answer      string          `json:"answer"`
	Analysis    string          `json:"analysis"`
	ScoringRule json.RawMessage `json:"scoringRule"`
}

func (s *Service) loadAttempt(ctx context.Context, q querier, id string) (*attemptRow, error) {
	var attempt attemptRow
	err := q.QueryRow(ctx, `
		select id, user_id::text, question_id, question_revision, selected, correct, assisted,
		       duration_ms, answered_at, session_id::text, created_at
		from attempts where id = $1`, id).
		Scan(&attempt.ID, &attempt.UserID, &attempt.QuestionID, &attempt.Revision, &attempt.Selected,
			&attempt.Correct, &attempt.Assisted, &attempt.DurationMS, &attempt.AnsweredAt,
			&attempt.SessionID, &attempt.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load attempt: %w", err)
	}
	return &attempt, nil
}

func (s *Service) questionVersion(ctx context.Context, q querier, questionID, revision string) (*questionVersion, error) {
	var version questionVersion
	err := q.QueryRow(ctx, `
		select v.question_id, v.revision, q.module, q.term, q.paper, q.question_no, q.knowledge,
		       q.difficulty, q.source_type, q.figure_missing, q.multi_blank,
		       v.stem, v.options, v.answer, v.analysis, v.scoring_rule
		from question_versions v
		join questions q on q.id = v.question_id
		where v.question_id = $1 and v.revision = $2`, questionID, revision).
		Scan(&version.QuestionID, &version.Revision, &version.Module, &version.Term, &version.Paper,
			&version.QuestionNo, &version.Knowledge, &version.Difficulty, &version.SourceType,
			&version.FigureMissing, &version.MultiBlank, &version.Stem, &version.Options,
			&version.Answer, &version.Analysis, &version.ScoringRule)
	if err != nil {
		return nil, mapLoadError(store.MapError(err))
	}
	return &version, nil
}

func (s *Service) questionVersions(ctx context.Context, q querier, questionIDs []string) (map[string]questionVersion, error) {
	versions := map[string]questionVersion{}
	if len(questionIDs) == 0 {
		return versions, nil
	}
	rows, err := q.Query(ctx, `
		select v.question_id, v.revision, q.module, q.term, q.paper, q.question_no, q.knowledge,
		       q.difficulty, q.source_type, q.figure_missing, q.multi_blank,
		       v.stem, v.options, v.answer, v.analysis, v.scoring_rule
		from question_versions v
		join questions q on q.id = v.question_id
		where v.question_id = any($1::text[])`, questionIDs)
	if err != nil {
		return nil, fmt.Errorf("load question versions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var version questionVersion
		if err := rows.Scan(&version.QuestionID, &version.Revision, &version.Module, &version.Term,
			&version.Paper, &version.QuestionNo, &version.Knowledge, &version.Difficulty,
			&version.SourceType, &version.FigureMissing, &version.MultiBlank,
			&version.Stem, &version.Options, &version.Answer, &version.Analysis,
			&version.ScoringRule); err != nil {
			return nil, err
		}
		versions[version.QuestionID+"\x00"+version.Revision] = version
	}
	return versions, rows.Err()
}

// sessionAttempts returns the latest attempt per question inside one session.
// When cutoff is set, only attempts received before the cutoff are considered
// (used when a late submission is graded on the answers already recorded).
func (s *Service) sessionAttempts(ctx context.Context, q querier, sessionID string, cutoff *time.Time) (map[string]attemptRow, error) {
	query := `
		select distinct on (question_id) id, user_id::text, question_id, question_revision,
		       selected, correct, assisted, duration_ms, answered_at, session_id::text, created_at
		from attempts
		where session_id = $1::uuid`
	args := []any{sessionID}
	if cutoff != nil {
		args = append(args, cutoff.UTC())
		query += " and answered_at <= $2"
	}
	query += " order by question_id, answered_at desc, created_at desc"
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("load session attempts: %w", err)
	}
	defer rows.Close()
	attempts := map[string]attemptRow{}
	for rows.Next() {
		var attempt attemptRow
		if err := rows.Scan(&attempt.ID, &attempt.UserID, &attempt.QuestionID, &attempt.Revision,
			&attempt.Selected, &attempt.Correct, &attempt.Assisted, &attempt.DurationMS,
			&attempt.AnsweredAt, &attempt.SessionID, &attempt.CreatedAt); err != nil {
			return nil, err
		}
		attempts[attempt.QuestionID] = attempt
	}
	return attempts, rows.Err()
}

// scoringRulePoints reads the points from a stored v1 scoring rule. Unknown
// shapes fall back to the v1 single-choice default of one point so a malformed
// rule cannot silently zero a learner's result.
func scoringRulePoints(raw []byte) int {
	var rule struct {
		Points      int    `json:"points"`
		RuleVersion string `json:"ruleVersion"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &rule) == nil && rule.Points > 0 {
		return rule.Points
	}
	return 1
}

func rawRule(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(defaultRuleJSON)
	}
	return json.RawMessage(append([]byte(nil), raw...))
}

// submitResult is one question in a submitted exam, including the pinned
// revision's answer and analysis.
type submitResult struct {
	QuestionID  string          `json:"questionId"`
	Revision    string          `json:"revision"`
	Selected    string          `json:"selected"`
	Correct     bool            `json:"correct"`
	Assisted    bool            `json:"assisted"`
	Points      int             `json:"points"`
	AnsweredAt  *time.Time      `json:"answeredAt"`
	Answer      string          `json:"answer"`
	Analysis    string          `json:"analysis"`
	ScoringRule json.RawMessage `json:"scoringRule"`
	Solution    *solutionView   `json:"solution,omitempty"`
}

// computeResults derives scores from recorded facts only. The scoring rule is
// read from the revision pinned when the session was created, so later question
// revisions can never rewrite a historical score.
func computeResults(record *sessionRecord, attempts map[string]attemptRow, versions map[string]questionVersion) ([]submitResult, int, int) {
	results := make([]submitResult, 0, len(record.QuestionIDs))
	score := 0
	maxScore := 0
	for _, questionID := range record.QuestionIDs {
		revision := record.Revisions[questionID]
		version := versions[questionID+"\x00"+revision]
		points := scoringRulePoints(version.ScoringRule)
		maxScore += points
		rule := rawRule(version.ScoringRule)
		item := submitResult{
			QuestionID:  questionID,
			Revision:    revision,
			Answer:      version.Answer,
			Analysis:    version.Analysis,
			ScoringRule: rule,
			Solution: &solutionView{
				Answer:      version.Answer,
				Analysis:    version.Analysis,
				ScoringRule: rule,
			},
		}
		if attempt, ok := attempts[questionID]; ok {
			answeredAt := attempt.AnsweredAt.UTC()
			item.Selected = attempt.Selected
			item.Correct = attempt.Correct
			item.Assisted = attempt.Assisted
			item.AnsweredAt = &answeredAt
			if attempt.Correct {
				item.Points = points
				score += points
			}
		}
		results = append(results, item)
	}
	return results, score, maxScore
}

// answerPayload assembles the documented answer response for both fresh and
// replayed events.
func (s *Service) answerPayload(ctx context.Context, q querier, record *sessionRecord, attempt *attemptRow) (map[string]any, error) {
	version, err := s.questionVersion(ctx, q, attempt.QuestionID, attempt.Revision)
	if err != nil {
		return nil, err
	}
	revealed := record.Mode != "exam" || record.SubmittedAt != nil
	var solution any
	if revealed {
		solution = solutionView{
			Answer:      version.Answer,
			Analysis:    version.Analysis,
			ScoringRule: rawRule(version.ScoringRule),
		}
	}
	view, err := s.sessionView(ctx, q, record)
	if err != nil {
		return nil, err
	}
	attemptPayload := attempt.view()
	if !revealed {
		// A formal exam must not leak correctness before submission.
		attemptPayload.Correct = nil
	}
	payload := map[string]any{
		"attempt":  attemptPayload,
		"revealed": revealed,
		"solution": solution,
		"session":  view,
	}
	if revealed {
		payload["correct"] = attempt.Correct
	}
	return payload, nil
}

func (s *Service) handleAnswer(w http.ResponseWriter, r *http.Request) {
	var request answerRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	request.ID = strings.TrimSpace(request.ID)
	request.QuestionID = strings.TrimSpace(request.QuestionID)
	if request.ID == "" {
		httpx.Error(w, r, badRequest("event_id_required", "缺少作答事件 ID。"))
		return
	}
	if len(request.ID) > 200 {
		httpx.Error(w, r, badRequest("event_id_invalid", "作答事件 ID 过长。"))
		return
	}
	if request.QuestionID == "" {
		httpx.Error(w, r, badRequest("question_required", "缺少题目 ID。"))
		return
	}
	selected := strings.TrimSpace(request.Selected)
	if selected == "" {
		httpx.Error(w, r, badRequest("answer_required", "请选择答案。"))
		return
	}
	if len(selected) > 32 {
		httpx.Error(w, r, badRequest("answer_invalid", "答案内容不正确。"))
		return
	}

	user := auth.MustUser(r.Context())
	sessionID := r.PathValue("id")

	var (
		payload map[string]any
		txErr   error
	)
	err := s.db.Tx(r.Context(), func(tx pgx.Tx) error {
		ctx := r.Context()
		record, err := s.loadSessionForUpdate(ctx, tx, user.ID, sessionID)
		if err != nil {
			return err
		}
		now, err := nowInTx(ctx, tx)
		if err != nil {
			return err
		}

		// Client event id is the idempotency key: a retry returns the original
		// attempt and never inserts a second fact.
		existing, err := s.loadAttempt(ctx, tx, request.ID)
		if err != nil {
			return err
		}
		if existing != nil {
			if existing.UserID != user.ID {
				return httpx.ErrNotFound
			}
			if existing.SessionID != nil && *existing.SessionID != record.ID {
				return badRequest("event_id_conflict", "作答事件 ID 已被其他现场使用。")
			}
			payload, txErr = s.answerPayload(ctx, tx, record, existing)
			return txErr
		}

		if record.Status != "active" {
			view, viewErr := s.sessionView(ctx, tx, record)
			if viewErr != nil {
				return viewErr
			}
			return errSessionFinished.WithDetails(map[string]any{"session": view})
		}
		if record.Mode == "exam" && record.DeadlineAt != nil && now.After(*record.DeadlineAt) {
			return errDeadlinePassed.WithDetails(map[string]any{"deadlineAt": utcPtr(record.DeadlineAt)})
		}
		revision, ok := record.Revisions[request.QuestionID]
		if !ok {
			return badRequest("question_not_in_session", "该题目不在本次练习范围内。")
		}
		version, err := s.questionVersion(ctx, tx, request.QuestionID, revision)
		if err != nil {
			return err
		}
		correct := strings.EqualFold(selected, strings.TrimSpace(version.Answer))

		answeredAt := now
		if raw := strings.TrimSpace(request.AnsweredAt); raw != "" {
			parsed, parseErr := time.Parse(time.RFC3339, raw)
			if parseErr != nil {
				return badRequest("invalid_answered_at", "作答时间格式不正确。")
			}
			if parsed.After(now) {
				answeredAt = now
			} else {
				answeredAt = parsed.UTC()
			}
		}
		durationMS := request.DurationMS
		if durationMS < 0 {
			durationMS = 0
		}
		_, err = tx.Exec(ctx, `
			insert into attempts (id, user_id, session_id, question_id, question_revision,
			                      selected, correct, assisted, duration_ms, answered_at)
			values ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9, $10)`,
			request.ID, user.ID, record.ID, request.QuestionID, revision, selected,
			correct, request.Assisted, durationMS, answeredAt)
		if err != nil {
			return fmt.Errorf("insert attempt: %w", err)
		}
		// Answering changes the session's observable state even though the
		// optimistic version is only for index/elapsed updates.
		if _, err := tx.Exec(ctx, `update practice_sessions set updated_at = now() where id = $1::uuid`, record.ID); err != nil {
			return err
		}
		if record.Mode != "exam" {
			if err := s.upsertReviewItem(ctx, tx, user.ID, request.QuestionID, request.ID, answeredAt, correct, request.Assisted); err != nil {
				return err
			}
		}
		created := &attemptRow{
			ID: request.ID, UserID: user.ID, QuestionID: request.QuestionID, Revision: revision,
			Selected: selected, Correct: correct, Assisted: request.Assisted,
			DurationMS: durationMS, AnsweredAt: answeredAt, SessionID: &record.ID,
		}
		if refreshed, loadErr := s.loadSession(ctx, tx, user.ID, record.ID); loadErr == nil {
			record = refreshed
		}
		payload, txErr = s.answerPayload(ctx, tx, record, created)
		return txErr
	})
	if err != nil {
		// A concurrent retry of the same event id may have won the insert race;
		// PostgreSQL aborts the losing transaction, so resolve it on the pool.
		if store.IsUniqueViolation(err) {
			record, loadErr := s.loadSession(r.Context(), s.db.Pool, user.ID, sessionID)
			if loadErr != nil {
				httpx.Error(w, r, loadErr)
				return
			}
			existing, loadErr := s.loadAttempt(r.Context(), s.db.Pool, request.ID)
			if loadErr != nil {
				httpx.Error(w, r, loadErr)
				return
			}
			if existing != nil && existing.UserID == user.ID && existing.SessionID != nil && *existing.SessionID == record.ID {
				payload, buildErr := s.answerPayload(r.Context(), s.db.Pool, record, existing)
				if buildErr != nil {
					httpx.Error(w, r, buildErr)
					return
				}
				httpx.JSON(w, r, http.StatusOK, payload)
				return
			}
		}
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, payload)
}

// upsertReviewItem records derived review state next to the attempt. The
// attempt itself remains the source of truth; review-due re-derives from facts.
func (s *Service) upsertReviewItem(ctx context.Context, tx pgx.Tx, userID, questionID, attemptID string, answeredAt time.Time, correct, assisted bool) error {
	interval := learning.ReviewIntervalDays(correct, assisted)
	dueAt := learning.ReviewDueAt(answeredAt, correct, assisted)
	reason, err := json.Marshal(map[string]any{
		"ruleVersion": learning.RuleVersion,
		"correct":     correct,
		"assisted":    assisted,
		"attemptId":   attemptID,
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		insert into review_items (user_id, question_id, due_at, interval_days, reason, updated_at)
		values ($1::uuid, $2, $3, $4, $5::jsonb, now())
		on conflict (user_id, question_id) do update
		set due_at = excluded.due_at, interval_days = excluded.interval_days,
		    reason = excluded.reason, updated_at = now()`,
		userID, questionID, dueAt, interval, reason)
	if err != nil {
		return fmt.Errorf("upsert review item: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- submit

func (s *Service) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Version *int `json:"version"`
	}
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	user := auth.MustUser(r.Context())
	sessionID := r.PathValue("id")

	var (
		record   *sessionRecord
		results  []submitResult
		score    int
		maxScore int
		late     bool
	)
	err := s.db.Tx(r.Context(), func(tx pgx.Tx) error {
		ctx := r.Context()
		current, err := s.loadSessionForUpdate(ctx, tx, user.ID, sessionID)
		if err != nil {
			return err
		}
		if current.Status == "abandoned" {
			view, viewErr := s.sessionView(ctx, tx, current)
			if viewErr != nil {
				return viewErr
			}
			return errSessionFinished.WithDetails(map[string]any{"session": view})
		}
		now, err := nowInTx(ctx, tx)
		if err != nil {
			return err
		}
		if current.Status == "finished" {
			// Submitting twice is idempotent: return the recorded result.
			attempts, err := s.sessionAttempts(ctx, tx, current.ID, nil)
			if err != nil {
				return err
			}
			versions, err := s.questionVersions(ctx, tx, current.QuestionIDs)
			if err != nil {
				return err
			}
			results, score, maxScore = computeResults(current, attempts, versions)
			record = current
			return nil
		}

		var cutoff *time.Time
		if current.Mode == "exam" && current.DeadlineAt != nil && now.After(*current.DeadlineAt) {
			// The deadline is authoritative server time. Grade only the answers
			// already received, then report deadline_passed.
			late = true
			cutoff = current.DeadlineAt
		}
		attempts, err := s.sessionAttempts(ctx, tx, current.ID, cutoff)
		if err != nil {
			return err
		}
		versions, err := s.questionVersions(ctx, tx, current.QuestionIDs)
		if err != nil {
			return err
		}
		for _, attempt := range attempts {
			if err := s.upsertReviewItem(ctx, tx, user.ID, attempt.QuestionID, attempt.ID, attempt.AnsweredAt, attempt.Correct, attempt.Assisted); err != nil {
				return err
			}
		}
		results, score, maxScore = computeResults(current, attempts, versions)
		row := tx.QueryRow(ctx, `
			update practice_sessions
			set status = 'finished', submitted_at = now(), score = $2, max_score = $3,
			    version = version + 1, updated_at = now()
			where id = $1::uuid
			returning `+sessionColumns, current.ID, score, maxScore)
		updated, err := scanSession(row)
		if err != nil {
			return err
		}
		record = updated
		return nil
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := s.sessionView(r.Context(), s.db.Pool, record)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	payload := map[string]any{
		"session":  view,
		"score":    score,
		"maxScore": maxScore,
		"results":  results,
		"revealed": true,
	}
	if late {
		httpx.Error(w, r, errDeadlinePassed.WithDetails(payload))
		return
	}
	httpx.JSON(w, r, http.StatusOK, payload)
}

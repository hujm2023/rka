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
	"rka/internal/store"
)

const sessionColumns = `id::text, user_id::text, mode, scope, question_ids, revisions, index,
	elapsed_seconds, status, bank_version, version, deadline_at, submitted_at, score, max_score,
	created_at, updated_at`

type sessionRecord struct {
	ID             string
	UserID         string
	Mode           string
	Scope          scope
	QuestionIDs    []string
	Revisions      map[string]string
	Index          int
	ElapsedSeconds int
	Status         string
	BankVersion    string
	Version        int
	DeadlineAt     *time.Time
	SubmittedAt    *time.Time
	Score          *int
	MaxScore       *int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func scanSession(row pgx.Row) (*sessionRecord, error) {
	var (
		record       sessionRecord
		scopeRaw     []byte
		revisionsRaw []byte
	)
	err := row.Scan(&record.ID, &record.UserID, &record.Mode, &scopeRaw, &record.QuestionIDs,
		&revisionsRaw, &record.Index, &record.ElapsedSeconds, &record.Status, &record.BankVersion,
		&record.Version, &record.DeadlineAt, &record.SubmittedAt, &record.Score, &record.MaxScore,
		&record.CreatedAt, &record.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if len(scopeRaw) > 0 {
		if err := json.Unmarshal(scopeRaw, &record.Scope); err != nil {
			return nil, fmt.Errorf("decode session scope: %w", err)
		}
	}
	if len(revisionsRaw) > 0 {
		if err := json.Unmarshal(revisionsRaw, &record.Revisions); err != nil {
			return nil, fmt.Errorf("decode session revisions: %w", err)
		}
	}
	if record.Revisions == nil {
		record.Revisions = map[string]string{}
	}
	return &record, nil
}

func (s *Service) loadSession(ctx context.Context, q querier, userID, id string) (*sessionRecord, error) {
	row := q.QueryRow(ctx, `select `+sessionColumns+` from practice_sessions where id = $1::uuid and user_id = $2::uuid`, id, userID)
	record, err := scanSession(row)
	if err != nil {
		return nil, mapLoadError(store.MapError(err))
	}
	return record, nil
}

func (s *Service) loadSessionForUpdate(ctx context.Context, tx pgx.Tx, userID, id string) (*sessionRecord, error) {
	row := tx.QueryRow(ctx, `select `+sessionColumns+` from practice_sessions where id = $1::uuid and user_id = $2::uuid for update`, id, userID)
	record, err := scanSession(row)
	if err != nil {
		return nil, mapLoadError(store.MapError(err))
	}
	return record, nil
}

func (s *Service) loadActiveSession(ctx context.Context, q querier, userID string) (*sessionRecord, error) {
	row := q.QueryRow(ctx, `select `+sessionColumns+` from practice_sessions where user_id = $1::uuid and status = 'active' order by created_at desc limit 1`, userID)
	record, err := scanSession(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load active session: %w", err)
	}
	return record, nil
}

// ---------------------------------------------------------------- views

type answerView struct {
	Selected   string        `json:"selected"`
	Submitted  bool          `json:"submitted"`
	Assisted   bool          `json:"assisted"`
	AttemptID  string        `json:"attemptId"`
	AnsweredAt time.Time     `json:"answeredAt"`
	Revealed   bool          `json:"revealed"`
	Correct    *bool         `json:"correct,omitempty"`
	Solution   *solutionView `json:"solution,omitempty"`
}

type sessionView struct {
	ID             string                `json:"id"`
	Mode           string                `json:"mode"`
	Scope          scope                 `json:"scope"`
	QuestionIDs    []string              `json:"questionIds"`
	Revisions      map[string]string     `json:"revisions"`
	Index          int                   `json:"index"`
	ElapsedSeconds int                   `json:"elapsedSeconds"`
	Status         string                `json:"status"`
	BankVersion    string                `json:"bankVersion"`
	Version        int                   `json:"version"`
	CreatedAt      time.Time             `json:"createdAt"`
	UpdatedAt      time.Time             `json:"updatedAt"`
	DeadlineAt     *time.Time            `json:"deadlineAt"`
	SubmittedAt    *time.Time            `json:"submittedAt"`
	Score          *int                  `json:"score"`
	MaxScore       *int                  `json:"maxScore"`
	Answers        map[string]answerView `json:"answers"`
}

type questionSummary struct {
	ID            string          `json:"id"`
	Revision      string          `json:"revision"`
	Module        string          `json:"module"`
	Term          string          `json:"term"`
	Paper         string          `json:"paper"`
	QuestionNo    int             `json:"questionNo"`
	Knowledge     string          `json:"knowledge"`
	Difficulty    string          `json:"difficulty"`
	SourceType    string          `json:"sourceType"`
	Stem          string          `json:"stem"`
	Options       json.RawMessage `json:"options"`
	FigureMissing bool            `json:"figureMissing"`
	MultiBlank    bool            `json:"multiBlank"`
}

func (s *Service) sessionView(ctx context.Context, q querier, record *sessionRecord) (sessionView, error) {
	answers, err := s.loadSessionAnswers(ctx, q, record.ID)
	if err != nil {
		return sessionView{}, err
	}
	// A formal exam hides correctness until it is submitted; practice reveals it
	// as soon as an answer is recorded.
	reveal := record.Mode != "exam" || record.SubmittedAt != nil
	if !reveal {
		for id, answer := range answers {
			answer.Correct = nil
			answer.Solution = nil
			answer.Revealed = false
			answers[id] = answer
		}
	} else if len(answers) > 0 {
		// Revealed answers carry the pinned revision's solution so a reload of
		// practice sessions (or a submitted exam) keeps the analysis visible.
		answered := make([]string, 0, len(answers))
		for id := range answers {
			answered = append(answered, id)
		}
		versions, err := s.questionVersions(ctx, q, answered)
		if err != nil {
			return sessionView{}, err
		}
		for id, answer := range answers {
			version, ok := versions[id+"\x00"+record.Revisions[id]]
			if !ok {
				continue
			}
			answer.Revealed = true
			answer.Solution = &solutionView{
				Answer:      version.Answer,
				Analysis:    version.Analysis,
				ScoringRule: rawRule(version.ScoringRule),
			}
			answers[id] = answer
		}
	}
	return sessionView{
		ID:             record.ID,
		Mode:           record.Mode,
		Scope:          record.Scope,
		QuestionIDs:    append([]string{}, record.QuestionIDs...),
		Revisions:      record.Revisions,
		Index:          record.Index,
		ElapsedSeconds: record.ElapsedSeconds,
		Status:         record.Status,
		BankVersion:    record.BankVersion,
		Version:        record.Version,
		CreatedAt:      record.CreatedAt.UTC(),
		UpdatedAt:      record.UpdatedAt.UTC(),
		DeadlineAt:     utcPtr(record.DeadlineAt),
		SubmittedAt:    utcPtr(record.SubmittedAt),
		Score:          record.Score,
		MaxScore:       record.MaxScore,
		Answers:        answers,
	}, nil
}

func (s *Service) loadSessionAnswers(ctx context.Context, q querier, sessionID string) (map[string]answerView, error) {
	rows, err := q.Query(ctx, `
		select distinct on (question_id) id, question_id, selected, correct, assisted, answered_at
		from attempts
		where session_id = $1::uuid
		order by question_id, answered_at desc, created_at desc`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load session answers: %w", err)
	}
	defer rows.Close()
	answers := map[string]answerView{}
	for rows.Next() {
		var item answerView
		var questionID string
		if err := rows.Scan(&item.AttemptID, &questionID, &item.Selected, &item.Correct, &item.Assisted, &item.AnsweredAt); err != nil {
			return nil, err
		}
		item.Submitted = true
		item.AnsweredAt = item.AnsweredAt.UTC()
		answers[questionID] = item
	}
	return answers, rows.Err()
}

func (s *Service) questionSummaries(ctx context.Context, q querier, record *sessionRecord) ([]questionSummary, error) {
	if len(record.QuestionIDs) == 0 {
		return []questionSummary{}, nil
	}
	rows, err := q.Query(ctx, `
		select q.id, v.revision, q.module, q.term, q.paper, q.question_no, q.knowledge,
		       q.difficulty, q.source_type, q.figure_missing, q.multi_blank, v.stem, v.options
		from questions q
		join question_versions v on v.question_id = q.id
		where v.question_id = any($1::text[])`, record.QuestionIDs)
	if err != nil {
		return nil, fmt.Errorf("load session questions: %w", err)
	}
	defer rows.Close()
	byKey := map[string]questionSummary{}
	for rows.Next() {
		var item questionSummary
		if err := rows.Scan(&item.ID, &item.Revision, &item.Module, &item.Term, &item.Paper,
			&item.QuestionNo, &item.Knowledge, &item.Difficulty, &item.SourceType,
			&item.FigureMissing, &item.MultiBlank, &item.Stem, &item.Options); err != nil {
			return nil, err
		}
		byKey[item.ID+"\x00"+item.Revision] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	summaries := make([]questionSummary, 0, len(record.QuestionIDs))
	for _, id := range record.QuestionIDs {
		key := id + "\x00" + record.Revisions[id]
		if summary, ok := byKey[key]; ok {
			if summary.Options == nil {
				summary.Options = json.RawMessage(`{}`)
			}
			summaries = append(summaries, summary)
		}
	}
	return summaries, nil
}

// ---------------------------------------------------------------- create

func (s *Service) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var request createSessionRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	user := auth.MustUser(r.Context())
	mode := strings.TrimSpace(request.Mode)
	if mode == "" {
		httpx.Error(w, r, badRequest("mode_required", "请选择练习方式。"))
		return
	}
	if !validMode(mode) {
		httpx.Error(w, r, badRequest("invalid_mode", "练习方式不正确。"))
		return
	}
	sc := normaliseScope(request.Scope)
	if err := validateScope(mode, sc); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if mode == "exam" {
		// Formal exams are always drawn from real / recalled papers, whatever
		// the browser omitted; the stored scope makes this explicit.
		sc.Source = "real"
	}
	if request.Count < 0 || request.Count > maxPracticeCount {
		httpx.Error(w, r, badRequest("invalid_count", "题目数量不正确。"))
		return
	}
	if request.DurationMinutes < 0 || request.DurationMinutes > maxExamMinutes {
		httpx.Error(w, r, badRequest("invalid_duration", "考试时长不正确。"))
		return
	}
	if sc.MaterialID != "" && !s.auth.CanAccessMaterial(r.Context(), user, sc.MaterialID) {
		httpx.Error(w, r, httpx.ErrForbidden)
		return
	}

	ctx := r.Context()
	now, err := s.db.Now(ctx)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	active, err := s.loadActiveSession(ctx, s.db.Pool, user.ID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if active != nil {
		view, viewErr := s.sessionView(ctx, s.db.Pool, active)
		if viewErr != nil {
			httpx.Error(w, r, viewErr)
			return
		}
		if sameRange(active.Mode, active.Scope, mode, sc) {
			httpx.JSON(w, r, http.StatusOK, map[string]any{"session": view})
			return
		}
		if !request.Replace {
			httpx.Error(w, r, errActiveSession.WithDetails(map[string]any{"session": view}))
			return
		}
	}

	limit := request.Count
	if limit == 0 && mode != "exam" {
		limit = defaultPracticeCount
	}
	candidates, err := s.selectQuestions(ctx, user.ID, mode, sc, limit, now)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if len(candidates) == 0 {
		httpx.Error(w, r, errNoQuestions)
		return
	}
	questionIDs := make([]string, 0, len(candidates))
	revisions := make(map[string]string, len(candidates))
	for _, item := range candidates {
		questionIDs = append(questionIDs, item.ID)
		revisions[item.ID] = item.Revision
	}
	bankVersion, err := s.bankVersion(ctx, s.db.Pool)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	record, err := s.insertSession(ctx, user.ID, mode, sc, questionIDs, revisions, bankVersion, request.DurationMinutes, active, request.Replace)
	if err != nil {
		if store.IsUniqueViolation(err) {
			// Another request created the active session first. Reuse it when it
			// is the same range; otherwise surface the documented conflict.
			current, loadErr := s.loadActiveSession(ctx, s.db.Pool, user.ID)
			if loadErr == nil && current != nil {
				view, viewErr := s.sessionView(ctx, s.db.Pool, current)
				if viewErr == nil {
					if sameRange(current.Mode, current.Scope, mode, sc) {
						httpx.JSON(w, r, http.StatusOK, map[string]any{"session": view})
						return
					}
					httpx.Error(w, r, errActiveSession.WithDetails(map[string]any{"session": view}))
					return
				}
			}
		}
		httpx.Error(w, r, err)
		return
	}
	view, err := s.sessionView(ctx, s.db.Pool, record)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusCreated, map[string]any{"session": view})
}

func (s *Service) insertSession(ctx context.Context, userID, mode string, sc scope, questionIDs []string, revisions map[string]string, bankVersion string, durationMinutes int, active *sessionRecord, replace bool) (*sessionRecord, error) {
	scopeRaw, err := json.Marshal(sc)
	if err != nil {
		return nil, err
	}
	revisionsRaw, err := json.Marshal(revisions)
	if err != nil {
		return nil, err
	}
	examMinutes := 0
	if mode == "exam" {
		examMinutes = durationMinutes
		if examMinutes == 0 {
			examMinutes = defaultExamMinutes
		}
	}
	var record *sessionRecord
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		if replace && active != nil {
			if _, err := tx.Exec(ctx, `
				update practice_sessions
				set status = 'abandoned', version = version + 1, updated_at = now()
				where id = $1::uuid and user_id = $2::uuid and status = 'active'`, active.ID, userID); err != nil {
				return fmt.Errorf("abandon active session: %w", err)
			}
		}
		row := tx.QueryRow(ctx, `
			insert into practice_sessions (user_id, mode, scope, question_ids, revisions, bank_version, deadline_at)
			values ($1::uuid, $2, $3::jsonb, $4, $5::jsonb, $6,
			        case when $7::int > 0 then now() + make_interval(mins => $7::int) else null end)
			returning `+sessionColumns,
			userID, mode, scopeRaw, questionIDs, revisionsRaw, bankVersion, examMinutes)
		inserted, err := scanSession(row)
		if err != nil {
			return err
		}
		record = inserted
		return nil
	})
	if err != nil {
		return nil, err
	}
	return record, nil
}

// ---------------------------------------------------------------- read / patch

func (s *Service) handleActiveSession(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	record, err := s.loadActiveSession(r.Context(), s.db.Pool, user.ID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if record == nil {
		httpx.JSON(w, r, http.StatusOK, map[string]any{"session": nil})
		return
	}
	view, err := s.sessionView(r.Context(), s.db.Pool, record)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"session": view})
}

func (s *Service) handleGetSession(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	record, err := s.loadSession(r.Context(), s.db.Pool, user.ID, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := s.sessionView(r.Context(), s.db.Pool, record)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	questions, err := s.questionSummaries(r.Context(), s.db.Pool, record)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"session": view, "questions": questions})
}

type patchSessionRequest struct {
	Index          *int `json:"index"`
	ElapsedSeconds *int `json:"elapsedSeconds"`
	Version        *int `json:"version"`
}

func (s *Service) handlePatchSession(w http.ResponseWriter, r *http.Request) {
	var request patchSessionRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if request.Version == nil {
		httpx.Error(w, r, badRequest("version_required", "缺少版本号，请刷新后重试。"))
		return
	}
	user := auth.MustUser(r.Context())
	sessionID := r.PathValue("id")

	var record *sessionRecord
	err := s.db.Tx(r.Context(), func(tx pgx.Tx) error {
		current, err := s.loadSessionForUpdate(r.Context(), tx, user.ID, sessionID)
		if err != nil {
			return err
		}
		if current.Status != "active" {
			view, viewErr := s.sessionView(r.Context(), tx, current)
			if viewErr != nil {
				return viewErr
			}
			return errSessionFinished.WithDetails(map[string]any{"session": view})
		}
		if current.Version != *request.Version {
			return httpx.ErrConflict.WithDetails(map[string]any{
				"serverVersion": current.Version,
				"serverValues": map[string]any{
					"index":          current.Index,
					"elapsedSeconds": current.ElapsedSeconds,
				},
			})
		}
		index := current.Index
		if request.Index != nil {
			if *request.Index < 0 || *request.Index >= len(current.QuestionIDs) {
				return badRequest("invalid_index", "题号超出范围。")
			}
			index = *request.Index
		}
		elapsed := current.ElapsedSeconds
		if request.ElapsedSeconds != nil {
			if *request.ElapsedSeconds < 0 {
				return badRequest("invalid_elapsed", "计时不正确。")
			}
			elapsed = *request.ElapsedSeconds
		}
		row := tx.QueryRow(r.Context(), `
			update practice_sessions
			set index = $2, elapsed_seconds = $3, version = version + 1, updated_at = now()
			where id = $1::uuid
			returning `+sessionColumns, sessionID, index, elapsed)
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
	httpx.JSON(w, r, http.StatusOK, map[string]any{"session": view})
}

// ---------------------------------------------------------------- finish

func (s *Service) handleFinish(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Version *int `json:"version"`
	}
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	user := auth.MustUser(r.Context())
	sessionID := r.PathValue("id")

	var record *sessionRecord
	err := s.db.Tx(r.Context(), func(tx pgx.Tx) error {
		current, err := s.loadSessionForUpdate(r.Context(), tx, user.ID, sessionID)
		if err != nil {
			return err
		}
		if current.Mode == "exam" && current.Status == "active" {
			return errExamRequiresSubmit
		}
		if current.Status == "abandoned" {
			view, viewErr := s.sessionView(r.Context(), tx, current)
			if viewErr != nil {
				return viewErr
			}
			return errSessionFinished.WithDetails(map[string]any{"session": view})
		}
		if current.Status == "finished" {
			record = current
			return nil
		}
		attempts, err := s.sessionAttempts(r.Context(), tx, current.ID, nil)
		if err != nil {
			return err
		}
		versions, err := s.questionVersions(r.Context(), tx, current.QuestionIDs)
		if err != nil {
			return err
		}
		_, score, maxScore := computeResults(current, attempts, versions)
		row := tx.QueryRow(r.Context(), `
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
	httpx.JSON(w, r, http.StatusOK, map[string]any{"session": view})
}

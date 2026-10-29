package content

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"rka/internal/httpx"
)

// resolveSnapshotDir finds the bundled content snapshot. The configured path is
// preferred; when it is empty (or relative to a test working directory) the
// repository tree is searched upwards for content/catalog.json.
func resolveSnapshotDir(preferred string) string {
	if preferred != "" {
		if _, err := os.Stat(filepath.Join(preferred, "catalog.json")); err == nil {
			return preferred
		}
	}
	if preferred == "" {
		preferred = "./content"
	}
	dir, err := os.Getwd()
	if err != nil {
		return preferred
	}
	for depth := 0; depth < 6; depth++ {
		candidate := filepath.Join(dir, "content")
		if _, err := os.Stat(filepath.Join(candidate, "catalog.json")); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return preferred
}

// ---------------------------------------------------------------- jobs

type jobView struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Status     string         `json:"status"`
	Stats      map[string]any `json:"stats"`
	Errors     []jobError     `json:"errors"`
	CreatedBy  string         `json:"createdBy,omitempty"`
	CreatedAt  time.Time      `json:"createdAt"`
	StartedAt  *time.Time     `json:"startedAt"`
	FinishedAt *time.Time     `json:"finishedAt"`
}

type contentImportRequest struct {
	SnapshotDir string `json:"snapshotDir"`
}

// handleAdminImport runs the snapshot import synchronously and returns the
// completed job. The import is bound to a context that survives the HTTP
// request so a dropped connection cannot leave a half-written snapshot behind
// without a job record.
func (s *Service) handleAdminImport(w http.ResponseWriter, r *http.Request) {
	var req contentImportRequest
	if r.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<10))
		if err != nil {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_request", "请求体读取失败。"))
			return
		}
		if len(strings.TrimSpace(string(raw))) > 0 {
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&req); err != nil {
				httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_request", "请求格式不正确："+err.Error()))
				return
			}
		}
	}
	snapshotDir := strings.TrimSpace(req.SnapshotDir)
	if snapshotDir == "" {
		snapshotDir = strings.TrimSpace(os.Getenv("RKA_CONTENT_SNAPSHOT"))
	}
	if snapshotDir == "" {
		snapshotDir = "./content"
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
	defer cancel()
	result, err := s.ImportSnapshot(ctx, snapshotDir, authUser(r).ID)
	if err != nil {
		httpx.Error(w, r, httpx.NewError(http.StatusInternalServerError, "import_failed",
			"内容导入失败："+err.Error()).WithDetails(map[string]any{"jobId": result.JobID}))
		return
	}
	job, err := s.loadJob(ctx, result.JobID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.record(r, "content.import", "job", result.JobID, map[string]any{
		"version": result.Version,
		"counts":  result.Counts,
		"errors":  len(result.Errors),
	})
	httpx.JSON(w, r, http.StatusOK, map[string]any{"job": job, "result": result})
}

func (s *Service) handleAdminJobs(w http.ResponseWriter, r *http.Request) {
	limit := intQuery(r.URL.Query().Get("limit"), 20, 1, 100)
	jobs, next, err := s.listJobs(r.Context(), limit, strings.TrimSpace(r.URL.Query().Get("cursor")))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"items": jobs, "nextCursor": next})
}

func (s *Service) handleAdminJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.loadJob(r.Context(), r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"job": job})
}

const jobColumns = `
	select j.id::text, j.kind, j.status, j.stats, j.errors,
	       coalesce(j.created_by::text, ''), j.created_at, j.started_at, j.finished_at
	from jobs j`

func (s *Service) loadJob(ctx context.Context, id string) (jobView, error) {
	if !uuidPattern.MatchString(id) {
		return jobView{}, httpx.NewError(http.StatusBadRequest, "bad_id", "任务 ID 不正确。")
	}
	row := s.db.Pool.QueryRow(ctx, jobColumns+` where j.id = $1::uuid`, id)
	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return jobView{}, httpx.ErrNotFound
	}
	if err != nil {
		return jobView{}, err
	}
	return job, nil
}

func (s *Service) listJobs(ctx context.Context, limit int, cursor string) ([]jobView, string, error) {
	args := []any{"content_import"}
	where := "j.kind = $1"
	if cursor != "" {
		createdAt, id, err := decodeJobCursor(cursor)
		if err != nil {
			return nil, "", httpx.NewError(http.StatusBadRequest, "bad_cursor", "分页游标不正确。")
		}
		args = append(args, createdAt, id)
		where += fmt.Sprintf(" and (j.created_at, j.id) < ($%d::timestamptz, $%d::uuid)", len(args)-1, len(args))
	}
	args = append(args, limit+1)
	rows, err := s.db.Pool.Query(ctx, jobColumns+` where `+where+`
		order by j.created_at desc, j.id desc limit $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, "", fmt.Errorf("query jobs: %w", err)
	}
	defer rows.Close()
	jobs := []jobView{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, "", err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(jobs) > limit {
		jobs = jobs[:limit]
		next = encodeJobCursor(jobs[len(jobs)-1])
	}
	return jobs, next, nil
}

func scanJob(row rowScanner) (jobView, error) {
	var job jobView
	var stats, jobErrors []byte
	if err := row.Scan(&job.ID, &job.Kind, &job.Status, &stats, &jobErrors, &job.CreatedBy,
		&job.CreatedAt, &job.StartedAt, &job.FinishedAt); err != nil {
		return jobView{}, err
	}
	job.Stats = map[string]any{}
	_ = json.Unmarshal(stats, &job.Stats)
	job.Errors = []jobError{}
	_ = json.Unmarshal(jobErrors, &job.Errors)
	job.CreatedAt = job.CreatedAt.UTC()
	job.StartedAt = utcPointer(job.StartedAt)
	job.FinishedAt = utcPointer(job.FinishedAt)
	return job, nil
}

func encodeJobCursor(job jobView) string {
	raw := job.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + job.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeJobCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", err
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", fmt.Errorf("invalid cursor")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", err
	}
	return createdAt, parts[1], nil
}

// ---------------------------------------------------------------- question revisions

type patchQuestionRequest struct {
	Stem          *string            `json:"stem"`
	Options       *map[string]string `json:"options"`
	Answer        *string            `json:"answer"`
	Analysis      *string            `json:"analysis"`
	ScoringRule   json.RawMessage    `json:"scoringRule"`
	SourceFile    *string            `json:"sourceFile"`
	SourceLabel   *string            `json:"sourceLabel"`
	SourceURL     *string            `json:"sourceUrl"`
	Figure        json.RawMessage    `json:"figure"`
	Revision      *string            `json:"revision"`
	Difficulty    *string            `json:"difficulty"`
	Knowledge     *string            `json:"knowledge"`
	Module        *string            `json:"module"`
	Term          *string            `json:"term"`
	QuestionNo    *int               `json:"questionNo"`
	FigureMissing *bool              `json:"figureMissing"`
	MultiBlank    *bool              `json:"multiBlank"`
}

type questionVersionContent struct {
	Stem        string
	Options     map[string]string
	Answer      string
	Analysis    string
	ScoringRule json.RawMessage
	SourceFile  string
	SourceLabel string
	SourceURL   string
	Figure      json.RawMessage
	Revision    string
}

// handleAdminPatchQuestion publishes a new immutable question revision. Old
// attempts keep their question_revision value, so historical scoring stays
// reproducible while the practice queue picks up the new content.
func (s *Service) handleAdminPatchQuestion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	questionID := r.PathValue("id")
	var req patchQuestionRequest
	if err := httpx.Decode(w, r, &req, 1<<20); err != nil {
		httpx.Error(w, r, err)
		return
	}
	current, err := s.loadQuestionVersionContent(ctx, questionID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if req.Stem != nil {
		current.Stem = strings.TrimSpace(*req.Stem)
	}
	if req.Options != nil {
		current.Options = *req.Options
	}
	if req.Answer != nil {
		current.Answer = strings.TrimSpace(*req.Answer)
	}
	if req.Analysis != nil {
		current.Analysis = *req.Analysis
	}
	if len(req.ScoringRule) > 0 {
		current.ScoringRule = req.ScoringRule
	}
	if req.SourceFile != nil {
		current.SourceFile = *req.SourceFile
	}
	if req.SourceLabel != nil {
		current.SourceLabel = *req.SourceLabel
	}
	if req.SourceURL != nil {
		current.SourceURL = *req.SourceURL
	}
	if len(req.Figure) > 0 {
		if string(req.Figure) == "null" {
			current.Figure = nil
		} else {
			current.Figure = req.Figure
		}
	}
	if current.Stem == "" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "missing_stem", "题干不能为空。"))
		return
	}
	if len(current.Options) < 2 {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_options", "选项至少需要两项。"))
		return
	}
	if _, ok := current.Options[current.Answer]; !ok {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_answer", "答案必须位于选项内。"))
		return
	}
	if len(current.ScoringRule) == 0 || string(current.ScoringRule) == "null" {
		current.ScoringRule = json.RawMessage(defaultScoringRule)
	}
	var validRule any
	if err := json.Unmarshal(current.ScoringRule, &validRule); err != nil {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_scoring_rule", "评分规则不是有效 JSON。"))
		return
	}
	revision := ""
	if req.Revision != nil {
		revision = strings.TrimSpace(*req.Revision)
	}
	if revision == "" {
		revision = revisionForQuestion(current.Stem, current.Options, current.Answer, current.Analysis)
	}
	optionsJSON, _ := json.Marshal(current.Options)
	var figure any
	if len(current.Figure) > 0 && string(current.Figure) != "null" {
		figure = pgJSON(current.Figure)
	}
	if req.Difficulty != nil || req.Knowledge != nil || req.Module != nil || req.Term != nil ||
		req.QuestionNo != nil || req.FigureMissing != nil || req.MultiBlank != nil {
		if _, err := s.db.Pool.Exec(ctx, `
			update questions set
				difficulty = coalesce($2, difficulty),
				knowledge = coalesce($3, knowledge),
				module = coalesce($4, module),
				term = coalesce($5, term),
				question_no = coalesce($6, question_no),
				figure_missing = coalesce($7, figure_missing),
				multi_blank = coalesce($8, multi_blank),
				updated_at = now()
			where id = $1`,
			questionID, req.Difficulty, req.Knowledge, req.Module, req.Term,
			req.QuestionNo, req.FigureMissing, req.MultiBlank); err != nil {
			httpx.Error(w, r, err)
			return
		}
	}
	created := revision != current.Revision
	if created {
		if _, err := s.db.Pool.Exec(ctx, `
			insert into question_versions
				(question_id, revision, stem, options, answer, analysis, scoring_rule,
				 source_file, source_label, source_url, figure)
			values ($1, $2, $3, $4::jsonb, $5, $6, $7::jsonb, $8, $9, $10, $11::jsonb)
			on conflict (question_id, revision) do nothing`,
			questionID, revision, cleanText(current.Stem), pgJSON(optionsJSON), cleanText(current.Answer), cleanText(current.Analysis),
			pgJSON(current.ScoringRule), cleanText(current.SourceFile), cleanText(current.SourceLabel), cleanText(current.SourceURL), figure); err != nil {
			httpx.Error(w, r, err)
			return
		}
		if _, err := s.db.Pool.Exec(ctx, `
			update questions q set current_version_id = v.id, updated_at = now()
			from question_versions v
			where q.id = $1 and v.question_id = $1 and v.revision = $2`,
			questionID, revision); err != nil {
			httpx.Error(w, r, err)
			return
		}
	}
	views, err := s.loadQuestionViews(ctx, "q.id = $1", []any{questionID}, "", 1)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if len(views) == 0 {
		httpx.Error(w, r, httpx.ErrNotFound)
		return
	}
	s.record(r, "content.question.revise", "question", questionID, map[string]any{
		"previousRevision": current.Revision, "revision": revision, "created": created,
	})
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"question": views[0], "revision": revision, "created": created,
	})
}

func (s *Service) loadQuestionVersionContent(ctx context.Context, questionID string) (questionVersionContent, error) {
	var content questionVersionContent
	var options, rule, figure []byte
	err := s.db.Pool.QueryRow(ctx, `
		select coalesce(v.revision, ''), coalesce(v.stem, ''), coalesce(v.options, '{}'),
		       coalesce(v.answer, ''), coalesce(v.analysis, ''), v.scoring_rule,
		       coalesce(v.source_file, ''), coalesce(v.source_label, ''), coalesce(v.source_url, ''), v.figure
		from questions q
		join question_versions v on v.id = q.current_version_id
		where q.id = $1`, questionID).
		Scan(&content.Revision, &content.Stem, &options, &content.Answer, &content.Analysis, &rule,
			&content.SourceFile, &content.SourceLabel, &content.SourceURL, &figure)
	if errors.Is(err, pgx.ErrNoRows) {
		return questionVersionContent{}, httpx.ErrNotFound
	}
	if err != nil {
		return questionVersionContent{}, err
	}
	if err := json.Unmarshal(options, &content.Options); err != nil {
		content.Options = map[string]string{}
	}
	content.ScoringRule = json.RawMessage(rule)
	if len(figure) > 0 {
		content.Figure = json.RawMessage(figure)
	}
	return content, nil
}

// ---------------------------------------------------------------- review states

type reviewRequest struct {
	ReviewStatus string `json:"reviewStatus"`
	// Status is accepted as an alias for clients that use the shorter key.
	Status string `json:"status"`
}

func (req reviewRequest) value() string {
	if req.ReviewStatus != "" {
		return req.ReviewStatus
	}
	return req.Status
}

func (s *Service) handleAdminReviewQuestion(w http.ResponseWriter, r *http.Request) {
	questionID := r.PathValue("id")
	var req reviewRequest
	if err := httpx.Decode(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	switch req.value() {
	case "published", "needs_review", "withdrawn":
	default:
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_status",
			"reviewStatus 只能是 published、needs_review 或 withdrawn。"))
		return
	}
	tag, err := s.db.Pool.Exec(r.Context(),
		`update questions set review_status = $2, updated_at = now() where id = $1`,
		questionID, req.value())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, r, httpx.ErrNotFound)
		return
	}
	views, err := s.loadQuestionViews(r.Context(), "q.id = $1", []any{questionID}, "", 1)
	if err != nil || len(views) == 0 {
		httpx.Error(w, r, httpx.ErrNotFound)
		return
	}
	s.record(r, "content.question.review", "question", questionID, map[string]any{"reviewStatus": req.value()})
	httpx.JSON(w, r, http.StatusOK, map[string]any{"question": views[0]})
}

func (s *Service) handleAdminReviewMaterial(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	materialID := r.PathValue("id")
	var req reviewRequest
	if err := httpx.Decode(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	switch req.value() {
	case "draft", "published", "withdrawn":
	default:
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_status",
			"reviewStatus 只能是 draft、published 或 withdrawn。"))
		return
	}
	tag, err := s.db.Pool.Exec(ctx,
		`update materials set review_status = $2, updated_at = now() where id = $1`,
		materialID, req.value())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, r, httpx.ErrNotFound)
		return
	}
	views, err := s.listMaterialViews(ctx, authUser(r), materialID)
	if err != nil || len(views) == 0 {
		httpx.Error(w, r, httpx.ErrNotFound)
		return
	}
	s.record(r, "content.material.review", "material", materialID, map[string]any{"reviewStatus": req.value()})
	httpx.JSON(w, r, http.StatusOK, map[string]any{"material": views[0]})
}

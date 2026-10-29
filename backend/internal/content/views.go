package content

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"rka/internal/auth"
)

// sectionView is the catalog/reader representation of one section. ID is the
// version-scoped anchor; Key is the cross-version stable key.
type sectionView struct {
	ID    string   `json:"id"`
	Key   string   `json:"key"`
	Title string   `json:"title"`
	Level int      `json:"level"`
	Start int      `json:"start"`
	Path  []string `json:"path"`
}

type materialView struct {
	ID               string        `json:"id"`
	Title            string        `json:"title"`
	Book             string        `json:"book"`
	Kind             string        `json:"kind"`
	Module           string        `json:"module"`
	SourceFile       string        `json:"sourceFile"`
	SourceURL        string        `json:"sourceUrl"`
	License          string        `json:"license"`
	Chars            int           `json:"chars"`
	Minutes          int           `json:"minutes"`
	Excerpt          string        `json:"excerpt"`
	SourceHash       string        `json:"sourceHash"`
	Version          int           `json:"version"`
	VersionID        string        `json:"versionId"`
	CurrentVersionID string        `json:"currentVersionId"`
	IndexStatus      string        `json:"indexStatus"`
	ReviewStatus     string        `json:"reviewStatus"`
	Status           string        `json:"status"`
	Sections         []sectionView `json:"sections"`
}

// questionView is the public question shape. It deliberately has no answer or
// analysis field: those only exist in questionSolution.
type questionView struct {
	ID            string            `json:"id"`
	Revision      string            `json:"revision"`
	SourceType    string            `json:"sourceType"`
	Term          string            `json:"term"`
	Paper         string            `json:"paper"`
	QuestionNo    int               `json:"questionNo"`
	Module        string            `json:"module"`
	Knowledge     string            `json:"knowledge"`
	Difficulty    string            `json:"difficulty"`
	Stem          string            `json:"stem"`
	Options       map[string]string `json:"options"`
	FigureMissing bool              `json:"figureMissing"`
	MultiBlank    bool              `json:"multiBlank"`
	Figure        json.RawMessage   `json:"figure"`
	SourceFile    string            `json:"sourceFile"`
	SourceLabel   string            `json:"sourceLabel"`
	SourceURL     string            `json:"sourceUrl"`
	ReviewStatus  string            `json:"reviewStatus"`
}

type questionSolution struct {
	QuestionID  string          `json:"questionId"`
	Revision    string          `json:"revision"`
	Answer      string          `json:"answer"`
	Analysis    string          `json:"analysis"`
	ScoringRule json.RawMessage `json:"scoringRule"`
}

type catalogLink struct {
	MaterialID   string   `json:"materialId"`
	SectionID    string   `json:"sectionId"`
	SectionKey   string   `json:"sectionKey"`
	SectionTitle string   `json:"sectionTitle"`
	SectionPath  []string `json:"sectionPath"`
	Terms        []string `json:"terms"`
	Score        float64  `json:"score"`
	Status       string   `json:"status"`
}

var chapterNumberPattern = regexp.MustCompile(`^第(\d+)章`)

// listMaterialViews returns materials visible to the user. Administrators see
// every review status; learners see published materials plus explicit grants.
func (s *Service) listMaterialViews(ctx context.Context, user auth.User, onlyID string) ([]materialView, error) {
	var (
		args       []any
		conditions []string
	)
	if !user.IsAdmin() {
		args = append(args, user.ID)
		conditions = append(conditions, fmt.Sprintf(`v.id is not null and (
			m.review_status = 'published'
			or exists (
				select 1 from content_grants g
				where g.user_id = $%d::uuid and g.revoked_at is null
				  and g.resource_type = 'material' and g.resource_id = m.id))`, len(args)))
	}
	if onlyID != "" {
		args = append(args, onlyID)
		conditions = append(conditions, fmt.Sprintf("m.id = $%d", len(args)))
	}
	where := "true"
	if len(conditions) > 0 {
		where = joinConditions(conditions)
	}
	rows, err := s.db.Pool.Query(ctx, `
		select m.id, m.title, m.book, m.kind, m.module, m.source_file, m.source_url, m.license,
		       coalesce(v.chars, 0), coalesce(v.minutes, 0), coalesce(v.excerpt, ''),
		       coalesce(v.sha256, ''), coalesce(v.version, 0), coalesce(v.index_status, 'pending'),
		       m.review_status, coalesce(v.id::text, '')
		from materials m
		left join material_versions v on v.id = m.current_version_id
		where `+where+`
		order by m.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query materials: %w", err)
	}
	defer rows.Close()
	views := []materialView{}
	versionIDs := []string{}
	for rows.Next() {
		var view materialView
		var versionID string
		if err := rows.Scan(&view.ID, &view.Title, &view.Book, &view.Kind, &view.Module,
			&view.SourceFile, &view.SourceURL, &view.License, &view.Chars, &view.Minutes,
			&view.Excerpt, &view.SourceHash, &view.Version, &view.IndexStatus, &view.ReviewStatus,
			&versionID); err != nil {
			return nil, err
		}
		view.Sections = []sectionView{}
		view.VersionID = versionID
		view.CurrentVersionID = versionID
		view.Status = view.ReviewStatus
		views = append(views, view)
		versionIDs = append(versionIDs, versionID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(views) == 0 {
		return views, nil
	}
	sections, err := s.sectionsForVersions(ctx, versionIDs)
	if err != nil {
		return nil, err
	}
	for i := range views {
		if list, ok := sections[versionIDs[i]]; ok {
			views[i].Sections = list
		}
	}
	sortMaterialViews(views)
	return views, nil
}

// sortMaterialViews keeps the reading order the content snapshot defines:
// tutorial chapters first (前言, 第1章 … 第20章), then the exam outline, then
// the remaining study notes. Without this the hash based ids would shuffle the
// library.
func sortMaterialViews(views []materialView) {
	seriesRank := func(book string) int {
		switch {
		case strings.HasPrefix(book, "教程知识点"):
			return 0
		case strings.HasPrefix(book, "考试大纲"):
			return 1
		default:
			return 2
		}
	}
	chapterNumber := func(title string) int {
		if strings.TrimSpace(title) == "前言" {
			return 0
		}
		match := chapterNumberPattern.FindStringSubmatch(title)
		if match == nil {
			return 1000
		}
		value, _ := strconv.Atoi(match[1])
		return value
	}
	sort.SliceStable(views, func(i, j int) bool {
		left, right := views[i], views[j]
		if l, r := seriesRank(left.Book), seriesRank(right.Book); l != r {
			return l < r
		}
		if l, r := chapterNumber(left.Title), chapterNumber(right.Title); l != r {
			return l < r
		}
		return left.Title < right.Title
	})
}

func joinConditions(conditions []string) string {
	out := conditions[0]
	for _, condition := range conditions[1:] {
		out += " and " + condition
	}
	return out
}

func (s *Service) sectionsForVersions(ctx context.Context, versionIDs []string) (map[string][]sectionView, error) {
	result := map[string][]sectionView{}
	if len(versionIDs) == 0 {
		return result, nil
	}
	rows, err := s.db.Pool.Query(ctx, `
		select material_version_id::text, anchor, stable_key, title, level, start_offset, path
		from material_sections
		where material_version_id::text = any($1::text[])
		order by start_offset`, versionIDs)
	if err != nil {
		return nil, fmt.Errorf("query sections: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var versionID string
		var view sectionView
		if err := rows.Scan(&versionID, &view.ID, &view.Key, &view.Title, &view.Level, &view.Start, &view.Path); err != nil {
			return nil, err
		}
		if view.Path == nil {
			view.Path = []string{}
		}
		result[versionID] = append(result[versionID], view)
	}
	return result, rows.Err()
}

// loadQuestionViews reads public question fields. `extraWhere` receives
// positional parameters starting at len(args)+1.
func (s *Service) loadQuestionViews(ctx context.Context, where string, args []any, order string, limit int) ([]questionView, error) {
	if order == "" {
		order = "q.id"
	}
	query := `
		select q.id, coalesce(v.revision, ''), q.source_type, q.term, q.paper, q.question_no,
		       q.module, q.knowledge, q.difficulty, coalesce(v.stem, ''), coalesce(v.options, '{}'),
		       q.figure_missing, q.multi_blank, v.figure, coalesce(v.source_file, ''),
		       coalesce(v.source_label, ''), coalesce(v.source_url, ''), q.review_status
		from questions q
		join question_versions v on v.id = q.current_version_id`
	if where != "" {
		query += " where " + where
	}
	query += " order by " + order
	if limit > 0 {
		query += fmt.Sprintf(" limit %d", limit)
	}
	rows, err := s.db.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query questions: %w", err)
	}
	defer rows.Close()
	views := []questionView{}
	for rows.Next() {
		view, err := scanQuestionView(rows)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanQuestionView(row rowScanner) (questionView, error) {
	var view questionView
	var options []byte
	var figure []byte
	if err := row.Scan(&view.ID, &view.Revision, &view.SourceType, &view.Term, &view.Paper,
		&view.QuestionNo, &view.Module, &view.Knowledge, &view.Difficulty, &view.Stem, &options,
		&view.FigureMissing, &view.MultiBlank, &figure, &view.SourceFile, &view.SourceLabel,
		&view.SourceURL, &view.ReviewStatus); err != nil {
		return questionView{}, err
	}
	if err := json.Unmarshal(options, &view.Options); err != nil {
		view.Options = map[string]string{}
	}
	if len(figure) > 0 && string(figure) != "null" {
		view.Figure = json.RawMessage(figure)
	}
	return view, nil
}

// loadQuestionSolution reads the answer-bearing columns for one question.
func (s *Service) loadQuestionSolution(ctx context.Context, questionID string) (questionSolution, error) {
	var solution questionSolution
	var rule []byte
	err := s.db.Pool.QueryRow(ctx, `
		select q.id, coalesce(v.revision, ''), coalesce(v.answer, ''), coalesce(v.analysis, ''), v.scoring_rule
		from questions q
		join question_versions v on v.id = q.current_version_id
		where q.id = $1`, questionID).
		Scan(&solution.QuestionID, &solution.Revision, &solution.Answer, &solution.Analysis, &rule)
	if err != nil {
		return questionSolution{}, err
	}
	if len(rule) > 0 {
		solution.ScoringRule = json.RawMessage(rule)
	}
	return solution, nil
}

// loadCatalogLinks returns approved and auto links bound to the currently
// published version of each accessible material. Rejected links and links to
// materials the user cannot read never reach readers.
func (s *Service) loadCatalogLinks(ctx context.Context, user auth.User) (map[string][]catalogLink, error) {
	var grantUser any
	if !user.IsAdmin() {
		grantUser = user.ID
	}
	rows, err := s.db.Pool.Query(ctx, `
		select l.question_id, l.material_id, s.anchor, s.stable_key, s.title, s.path,
		       l.terms, l.score, l.status
		from knowledge_links l
		join materials m on m.id = l.material_id
		join material_sections s on s.id = l.section_id
		where l.material_version_id = m.current_version_id
		  and l.status <> 'rejected'
		  and ($1 or m.review_status = 'published'
		       or exists (
		           select 1 from content_grants g
		           where g.user_id = $2::uuid and g.revoked_at is null
		             and g.resource_type = 'material' and g.resource_id = m.id))
		order by l.question_id, l.score desc`, user.IsAdmin(), grantUser)
	if err != nil {
		return nil, fmt.Errorf("query knowledge links: %w", err)
	}
	defer rows.Close()
	links := map[string][]catalogLink{}
	for rows.Next() {
		var questionID string
		var link catalogLink
		if err := rows.Scan(&questionID, &link.MaterialID, &link.SectionID, &link.SectionKey,
			&link.SectionTitle, &link.SectionPath, &link.Terms, &link.Score, &link.Status); err != nil {
			return nil, err
		}
		if link.SectionPath == nil {
			link.SectionPath = []string{}
		}
		if link.Terms == nil {
			link.Terms = []string{}
		}
		links[questionID] = append(links[questionID], link)
	}
	return links, rows.Err()
}

// loadSubjective returns cases or essays as payload objects with column values
// applied on top. The payload keeps the existing front-end field names
// (reference_answer, question_label), while camelCase aliases are added for the
// documented contract.
func (s *Service) loadSubjective(ctx context.Context, kind string) ([]map[string]any, error) {
	rows, err := s.db.Pool.Query(ctx, `
		select id, revision, title, module, term, paper, source_type, source_url, payload
		from subjective_items where kind = $1 order by id`, kind)
	if err != nil {
		return nil, fmt.Errorf("query %s items: %w", kind, err)
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, revision, title, module, term, paper, sourceType, sourceURL string
		var payload []byte
		if err := rows.Scan(&id, &revision, &title, &module, &term, &paper, &sourceType, &sourceURL, &payload); err != nil {
			return nil, err
		}
		item := map[string]any{}
		if len(payload) > 0 {
			_ = json.Unmarshal(payload, &item)
		}
		item["id"] = id
		item["revision"] = revision
		item["title"] = title
		item["module"] = module
		item["term"] = term
		item["paper"] = paper
		item["sourceType"] = sourceType
		item["sourceUrl"] = sourceURL
		if kind == "case" {
			if subQuestions, ok := item["subQuestions"].([]any); ok {
				for _, entry := range subQuestions {
					question, ok := entry.(map[string]any)
					if !ok {
						continue
					}
					if value, ok := question["reference_answer"]; ok {
						question["referenceAnswer"] = value
					}
					if value, ok := question["question_label"]; ok {
						question["questionLabel"] = value
					}
				}
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

// utcPointer normalises database timestamps to UTC for RFC 3339 responses.
func utcPointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

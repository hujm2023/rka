package practice_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"rka/internal/app"
	"rka/internal/store"
	"rka/internal/testutil"
)

const (
	learnerEmail    = "learner@example.com"
	learnerPassword = "rka-test-password-123"

	questionA         = "q-a"
	questionB         = "q-b"
	questionC         = "q-c"
	questionD         = "q-d"
	questionMissing   = "q-missing"
	questionWithdrawn = "q-withdrawn"

	termExam  = "2026年5月 模拟卷1"
	termOther = "2025年11月 模拟卷"
)

type testQuestion struct {
	ID            string
	Revision      string
	Term          string
	Module        string
	SourceType    string
	QuestionNo    int
	Answer        string
	Analysis      string
	Options       map[string]string
	FigureMissing bool
	MultiBlank    bool
	ReviewStatus  string
	Points        int
}

type materialFixture struct {
	ID       string
	Version  string
	Sections map[string]string // anchor -> section uuid
}

type contentFixture struct {
	MaterialOne materialFixture
	MaterialTwo materialFixture
	Questions   map[string]testQuestion
}

func (f contentFixture) answer(questionID string) string {
	question, ok := f.Questions[questionID]
	if !ok {
		return ""
	}
	return question.Answer
}

func (f contentFixture) revision(questionID string) string {
	question, ok := f.Questions[questionID]
	if !ok || question.Revision == "" {
		return "rev1"
	}
	return question.Revision
}

func newTestEnv(t *testing.T) (*app.App, *testutil.Server, *store.DB, contentFixture) {
	t.Helper()
	application, server, db := testutil.NewApp(t)
	fixture := seedContent(t, db)
	testutil.CreateUser(t, application, learnerEmail, learnerPassword, "learner")
	testutil.Login(t, server, learnerEmail, learnerPassword)
	return application, server, db, fixture
}

func seedContent(t *testing.T, db *store.DB) contentFixture {
	t.Helper()
	ctx := context.Background()

	materialOne := seedMaterial(t, db, "material-one", "第6章 数据库设计基础知识", map[string]string{
		"section-1": "6.1 数据库基础",
		"section-2": "6.2 关系数据库",
	})
	materialTwo := seedMaterial(t, db, "material-two", "第8章 架构设计基础知识", map[string]string{
		"section-8": "8.1 架构风格",
	})

	questions := []testQuestion{
		{ID: questionA, QuestionNo: 1, Term: termExam, Module: "database", Answer: "B", Analysis: "解析 A：正确选项为 B。", Options: map[string]string{"A": "1", "B": "2", "C": "3", "D": "4"}},
		{ID: questionB, QuestionNo: 2, Term: termExam, Module: "database", Answer: "C", Analysis: "解析 B：正确选项为 C。"},
		{ID: questionC, QuestionNo: 1, Term: termOther, Module: "architecture", SourceType: "mock", Answer: "A", Analysis: "解析 C：正确选项为 A。"},
		{ID: questionD, QuestionNo: 2, Term: termOther, Module: "architecture", Answer: "D", Analysis: "解析 D：正确选项为 D。"},
		{ID: questionMissing, QuestionNo: 3, Term: termExam, Module: "database", FigureMissing: true},
		{ID: questionWithdrawn, QuestionNo: 4, Term: termExam, Module: "database", ReviewStatus: "withdrawn"},
	}
	for _, question := range questions {
		seedQuestion(t, db, question)
	}

	links := map[string][]struct {
		material *materialFixture
		section  string
	}{
		questionA: {{material: &materialOne, section: "section-1"}},
		questionB: {{material: &materialOne, section: "section-2"}},
		questionC: {{material: &materialTwo, section: "section-8"}},
		questionD: {{material: &materialTwo, section: "section-8"}},
	}
	for questionID, specs := range links {
		for _, spec := range specs {
			_, err := db.Pool.Exec(ctx, `
				insert into knowledge_links (question_id, material_id, material_version_id, section_id,
				                             section_stable_key, terms, score, status)
				values ($1, $2, $3::uuid, $4::uuid, $5, '{}', 60, 'auto')`,
				questionID, spec.material.ID, spec.material.Version,
				spec.material.Sections[spec.section], "sec-"+spec.section)
			if err != nil {
				t.Fatalf("insert knowledge link %s: %v", questionID, err)
			}
		}
	}

	byID := map[string]testQuestion{}
	for _, question := range questions {
		byID[question.ID] = question
	}
	return contentFixture{MaterialOne: materialOne, MaterialTwo: materialTwo, Questions: byID}
}

func seedMaterial(t *testing.T, db *store.DB, id, title string, sections map[string]string) materialFixture {
	t.Helper()
	ctx := context.Background()
	sha := fmt.Sprintf("%s-%d", id, time.Now().UnixNano())
	if _, err := db.Pool.Exec(ctx, `insert into blobs (sha256, size, rel_path) values ($1, 100, $2)`,
		sha, "originals/materials/"+id+".md"); err != nil {
		t.Fatalf("insert blob: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		insert into materials (id, title, book, kind, module, source_file, source_url, license)
		values ($1, $2, '测试教材', 'textbook', 'database', $3, '', '')`,
		id, title, "content/"+id+".md"); err != nil {
		t.Fatalf("insert material: %v", err)
	}
	var versionID string
	if err := db.Pool.QueryRow(ctx, `
		insert into material_versions (material_id, version, sha256, chars, minutes, excerpt, index_status, published_at)
		values ($1, 1, $2, 1000, 10, '摘要', 'ready', now())
		returning id::text`, id, sha).Scan(&versionID); err != nil {
		t.Fatalf("insert material version: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `update materials set current_version_id = $2::uuid where id = $1`, id, versionID); err != nil {
		t.Fatalf("set current material version: %v", err)
	}
	sectionIDs := map[string]string{}
	for anchor, sectionTitle := range sections {
		var sectionID string
		if err := db.Pool.QueryRow(ctx, `
			insert into material_sections (material_version_id, anchor, stable_key, title, level, start_offset, path)
			values ($1::uuid, $2, $3, $4, 2, 0, '{}')
			returning id::text`, versionID, anchor, "sec-"+anchor, sectionTitle).Scan(&sectionID); err != nil {
			t.Fatalf("insert material section: %v", err)
		}
		sectionIDs[anchor] = sectionID
	}
	return materialFixture{ID: id, Version: versionID, Sections: sectionIDs}
}

func seedQuestion(t *testing.T, db *store.DB, question testQuestion) {
	t.Helper()
	ctx := context.Background()
	if question.SourceType == "" {
		question.SourceType = "real"
	}
	if question.Module == "" {
		question.Module = "database"
	}
	if question.ReviewStatus == "" {
		question.ReviewStatus = "published"
	}
	if question.Revision == "" {
		question.Revision = "rev1"
	}
	if question.Answer == "" {
		question.Answer = "B"
	}
	if question.Analysis == "" {
		question.Analysis = "解析：" + question.ID
	}
	if question.Options == nil {
		question.Options = map[string]string{"A": "选项A", "B": "选项B", "C": "选项C", "D": "选项D"}
	}
	points := question.Points
	if points <= 0 {
		points = 1
	}
	optionsJSON, err := json.Marshal(question.Options)
	if err != nil {
		t.Fatalf("encode options: %v", err)
	}
	ruleJSON := fmt.Sprintf(`{"type":"single-choice","points":%d,"ruleVersion":"v1"}`, points)
	if _, err := db.Pool.Exec(ctx, `
		insert into questions (id, source_type, term, paper, question_no, module, knowledge, difficulty,
		                       figure_missing, multi_blank, review_status)
		values ($1, $2, $3, $3, $4, $5, '测试知识点', '★', $6, $7, $8)`,
		question.ID, question.SourceType, question.Term, question.QuestionNo, question.Module,
		question.FigureMissing, question.MultiBlank, question.ReviewStatus); err != nil {
		t.Fatalf("insert question %s: %v", question.ID, err)
	}
	var versionID string
	if err := db.Pool.QueryRow(ctx, `
		insert into question_versions (question_id, revision, stem, options, answer, analysis, scoring_rule)
		values ($1, $2, $3, $4::jsonb, $5, $6, $7::jsonb)
		returning id::text`,
		question.ID, question.Revision, "题干："+question.ID, optionsJSON, question.Answer,
		question.Analysis, ruleJSON).Scan(&versionID); err != nil {
		t.Fatalf("insert question version %s: %v", question.ID, err)
	}
	if _, err := db.Pool.Exec(ctx, `update questions set current_version_id = $2::uuid where id = $1`,
		question.ID, versionID); err != nil {
		t.Fatalf("set current question version %s: %v", question.ID, err)
	}
}

// ---------------------------------------------------------------- HTTP helpers

func mustMap(t *testing.T, value any) map[string]any {
	t.Helper()
	typed, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected object, got %T (%v)", value, value)
	}
	return typed
}

func mustList(t *testing.T, value any) []any {
	t.Helper()
	typed, ok := value.([]any)
	if !ok {
		t.Fatalf("expected array, got %T (%v)", value, value)
	}
	return typed
}

func mustString(t *testing.T, value any) string {
	t.Helper()
	typed, ok := value.(string)
	if !ok {
		t.Fatalf("expected string, got %T (%v)", value, value)
	}
	return typed
}

func mustFloat(t *testing.T, value any) float64 {
	t.Helper()
	typed, ok := value.(float64)
	if !ok {
		t.Fatalf("expected number, got %T (%v)", value, value)
	}
	return typed
}

func mustInt(t *testing.T, value any) int {
	t.Helper()
	return int(mustFloat(t, value))
}

func sessionFrom(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	return mustMap(t, body["session"])
}

func errorFrom(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	return mustMap(t, body["error"])
}

func questionIDList(t *testing.T, session map[string]any) []string {
	t.Helper()
	raw := mustList(t, session["questionIds"])
	ids := make([]string, 0, len(raw))
	for _, item := range raw {
		ids = append(ids, mustString(t, item))
	}
	return ids
}

func createSession(t *testing.T, server *testutil.Server, payload map[string]any, wantStatus int) map[string]any {
	t.Helper()
	status, body := server.Do(t, http.MethodPost, "/api/practice/sessions", payload)
	testutil.MustStatus(t, wantStatus, status, body)
	return sessionFrom(t, body)
}

func submitAnswer(t *testing.T, server *testutil.Server, sessionID string, payload map[string]any, wantStatus int) map[string]any {
	t.Helper()
	status, body := server.Do(t, http.MethodPost, "/api/practice/sessions/"+sessionID+"/answers", payload)
	testutil.MustStatus(t, wantStatus, status, body)
	return body
}

func answerPayload(eventID, questionID, selected string) map[string]any {
	return map[string]any{
		"id":         eventID,
		"questionId": questionID,
		"selected":   selected,
		"assisted":   false,
		"durationMs": 1500,
		"answeredAt": time.Now().UTC().Format(time.RFC3339),
	}
}

func countRows(t *testing.T, db *store.DB, query string, args ...any) int {
	t.Helper()
	var count int
	if err := db.Pool.QueryRow(context.Background(), query, args...).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	return count
}

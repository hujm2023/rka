package content_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rka/internal/app"
	"rka/internal/content"
	"rka/internal/store"
	"rka/internal/testutil"
)

const (
	adminEmail    = "admin@example.com"
	adminPassword = "admin-password-123"
	learnerEmail  = "learner@example.com"
	userPassword  = "learner-password-123"
	otherEmail    = "other@example.com"
)

type testEnv struct {
	app       *app.App
	db        *store.DB
	admin     *testutil.Server
	learner   *testutil.Server
	other     *testutil.Server
	adminID   string
	learnerID string
	otherID   string
}

// newTestEnv builds one application with three cookie-isolated browser
// sessions: administrator, learner and a second learner.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	application, _, db := testutil.NewApp(t)
	adminServer := mustServer(t, application)
	learnerServer := mustServer(t, application)
	otherServer := mustServer(t, application)

	testutil.CreateUser(t, application, adminEmail, adminPassword, "admin")
	testutil.CreateUser(t, application, learnerEmail, userPassword, "learner")
	testutil.CreateUser(t, application, otherEmail, userPassword, "learner")

	admin := testutil.Login(t, adminServer, adminEmail, adminPassword)
	learner := testutil.Login(t, learnerServer, learnerEmail, userPassword)
	other := testutil.Login(t, otherServer, otherEmail, userPassword)
	return &testEnv{
		app: application, db: db,
		admin: adminServer, learner: learnerServer, other: otherServer,
		adminID: admin.ID, learnerID: learner.ID, otherID: other.ID,
	}
}

func mustServer(t *testing.T, application *app.App) *testutil.Server {
	t.Helper()
	_, server, _ := testutil.NewAppFor(t, application)
	return server
}

// writeFixture creates a small but complete snapshot directory. mutate shifts
// the section offsets of material-a while keeping every heading title, which is
// exactly the case stable_key must survive.
func writeFixture(t *testing.T, version string, mutate bool) string {
	t.Helper()
	materialA := "# 数据库系统\n\n## 1. 关系数据库\n\n关系数据库使用范式来减少冗余。第一范式要求属性不可再分。\n\n## 2. 数据库设计\n\n数据库设计包括需求分析与概念设计。\n\n### 2.1 概念设计\n\n概念设计使用 E-R 模型。\n"
	if mutate {
		materialA = strings.Replace(materialA, "## 1. 关系数据库",
			"本章补充一段说明文字。\n\n## 1. 关系数据库", 1)
	}
	materialB := "# 操作系统\n\n## 1. 进程管理\n\n进程同步与互斥是操作系统的重要内容。信号量用于实现互斥。\n"

	sectionsA := content.ParseSections(materialA)
	sectionsB := content.ParseSections(materialB)

	catalog := map[string]any{
		"version": version,
		"choices": []map[string]any{
			{
				"id": "q-1", "sourceType": "mock", "term": "2026年5月 模拟卷1", "paper": "模拟卷1",
				"questionNo": 1, "module": "database", "knowledge": "数据库—范式", "difficulty": "★",
				"stem":    "关系数据库的范式设计主要目的是（ ）。",
				"options": map[string]string{"A": "增加冗余", "B": "减少冗余", "C": "提升并发", "D": "减少索引"},
				"answer":  "B", "analysis": "范式化可以减少数据冗余与更新异常。",
				"sourceFile": "fixture/q1.md", "sourceUrl": "https://example.com/q1", "sourceLabel": "测试题库",
				"figure": nil, "revision": "rev-1", "figureMissing": false, "multiBlank": false,
			},
			{
				"id": "q-2", "sourceType": "real", "term": "2025年11月 真题", "paper": "真题",
				"questionNo": 2, "module": "computer_foundation", "knowledge": "", "difficulty": "★★",
				"stem":    "进程同步与互斥中，互斥信号量的初始值为（ ）。",
				"options": map[string]string{"A": "0", "B": "1", "C": "5", "D": "6"},
				"answer":  "B", "analysis": "互斥信号量初始值为 1。",
				"sourceFile": "fixture/q2.md", "sourceUrl": "https://example.com/q2", "sourceLabel": "测试题库",
				"revision": "rev-2", "figureMissing": true, "multiBlank": false,
			},
		},
		"cases": []map[string]any{
			{
				"id": "case-1", "sourceType": "mock", "term": "2026年5月 模拟卷1", "paper": "模拟卷1",
				"module": "architecture", "title": "试题一", "description": "案例描述",
				"subQuestions": []map[string]any{
					{"question_label": "问题1", "prompt": "请说明架构风格。", "reference_answer": "参考: 分层架构。"},
				},
				"sourceFile": "fixture/case1.md", "revision": "case-rev-1", "sourceUrl": "https://example.com/case1",
			},
		},
		"essays": []map[string]any{
			{
				"id": "essay-1", "sourceType": "real", "term": "2024年下半年", "paper": "2024年下半年",
				"module": "architecture", "title": "论微服务架构的设计与实践",
				"prompt": "请围绕微服务架构论述。", "writingPoints": "1. 项目背景\n2. 架构设计",
				"sourceFile": "fixture/essay1.md", "revision": "essay-rev-1", "sourceUrl": "https://example.com/essay1",
			},
		},
		"materials": []map[string]any{
			materialEntry("material-a", "数据库系统", "database", "textbook", materialA, sectionsA),
			materialEntry("material-b", "操作系统", "computer_foundation", "notes", materialB, sectionsB),
		},
		"knowledgeLinks": map[string]any{
			"q-1": []map[string]any{
				{
					"materialId":   "material-a",
					"sectionId":    sectionByTitle(t, sectionsA, "1. 关系数据库").Anchor,
					"sectionTitle": "1. 关系数据库",
					"sectionPath":  []string{sectionsA[0].Anchor},
					"terms":        []string{"范式", "关系数据库"},
					"score":        73.7,
				},
			},
		},
	}
	return writeRawFixture(t, catalog, map[string]string{
		"material-a": materialA,
		"material-b": materialB,
	})
}

func materialEntry(id, title, module, kind, markdown string, sections []content.Section) map[string]any {
	sectionList := make([]map[string]any, 0, len(sections))
	for _, section := range sections {
		sectionList = append(sectionList, map[string]any{
			"id": section.Anchor, "title": section.Title, "level": section.Level, "start": section.Start,
		})
	}
	sum := sha256.Sum256([]byte(markdown))
	return map[string]any{
		"id": id, "title": title, "book": "测试教材", "kind": kind, "module": module,
		"sourceFile": "fixture/" + id + ".md", "sourceUrl": "https://example.com/" + id,
		"license": "测试许可", "chars": len([]rune(markdown)), "minutes": 1,
		"sections": sectionList, "sourceHash": hex.EncodeToString(sum[:]), "excerpt": "测试资料",
	}
}

func sectionByTitle(t *testing.T, sections []content.Section, title string) content.Section {
	t.Helper()
	for _, section := range sections {
		if section.Title == title {
			return section
		}
	}
	t.Fatalf("section %q not found", title)
	return content.Section{}
}

// writeRawFixture writes a catalog plus material markdown files.
func writeRawFixture(t *testing.T, catalog map[string]any, materials map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatalf("marshal fixture catalog: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "catalog.json"), raw, 0o600); err != nil {
		t.Fatalf("write fixture catalog: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "materials"), 0o750); err != nil {
		t.Fatalf("create fixture materials: %v", err)
	}
	for id, body := range materials {
		if err := os.WriteFile(filepath.Join(dir, "materials", id+".md"), []byte(body), 0o600); err != nil {
			t.Fatalf("write fixture material %s: %v", id, err)
		}
	}
	return dir
}

// response reads the raw HTTP body, for endpoints that are not JSON or where
// the exact bytes matter (e.g. checking that answers are absent).
func response(t *testing.T, server *testutil.Server, path string) (int, http.Header, string) {
	t.Helper()
	res, err := server.Client.Get(server.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, string(raw)
}

func postJSON(t *testing.T, server *testutil.Server, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, server.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := server.Client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	decoded := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &decoded)
	}
	return res.StatusCode, decoded
}

func scalarInt(t *testing.T, db *store.DB, query string, args ...any) int {
	t.Helper()
	var value int
	if err := db.Pool.QueryRow(t.Context(), query, args...).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return value
}

func scalarString(t *testing.T, db *store.DB, query string, args ...any) string {
	t.Helper()
	var value string
	if err := db.Pool.QueryRow(t.Context(), query, args...).Scan(&value); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return value
}

// insertAttempt writes an answered-attempt fact directly, mirroring what the
// practice package writes, so content permission tests do not depend on T3.
func insertAttempt(t *testing.T, db *store.DB, userID, questionID, revision, selected string, correct bool) {
	t.Helper()
	_, err := db.Pool.Exec(t.Context(), `
		insert into attempts (id, user_id, question_id, question_revision, selected, correct, answered_at)
		values ($1, $2::uuid, $3, $4, $5, $6, now())`,
		"attempt-"+questionID+"-"+selected, userID, questionID, revision, selected, correct)
	if err != nil {
		t.Fatalf("insert attempt: %v", err)
	}
}

// insertExamSession creates an active exam containing the question, as the
// practice package would when a learner starts a mock exam.
func insertExamSession(t *testing.T, db *store.DB, userID string, questionIDs ...string) string {
	t.Helper()
	var id string
	err := db.Pool.QueryRow(t.Context(), `
		insert into practice_sessions (user_id, mode, scope, question_ids, revisions, status)
		values ($1::uuid, 'exam', '{}', $2, '{}', 'active')
		returning id::text`, userID, questionIDs).Scan(&id)
	if err != nil {
		t.Fatalf("insert exam session: %v", err)
	}
	return id
}

func containsAnswerFields(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "answer" || key == "analysis" || key == "referenceAnswer" {
				return true
			}
			if containsAnswerFields(child) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if containsAnswerFields(child) {
				return true
			}
		}
	}
	return false
}

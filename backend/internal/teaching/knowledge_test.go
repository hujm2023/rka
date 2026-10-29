package teaching_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"rka/internal/teaching"
	"rka/internal/testutil"
)

func TestNativeIndexResumesAndPublishesOnlyValidatedOriginal(t *testing.T) {
	var mu sync.Mutex
	created, validated := false, false
	uploads := 0
	original := "# 中文教材\n\n数据库事务保持一致性。\n"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "dt_token", Value: "index-test-token"})
			fmt.Fprint(w, `{"ok":true}`)
		case r.URL.Path == "/api/settings/catalog":
			fmt.Fprint(w, `{"catalog":{"services":{}}}`)
		case r.URL.Path == "/api/settings/apply/service" || r.URL.Path == "/api/knowledge-bases/rag-pipelines/llamaindex/config":
			fmt.Fprint(w, `{}`)
		case r.URL.Path == "/api/knowledge-bases" && r.Method == http.MethodPost:
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			defer r.MultipartForm.RemoveAll()
			file, _, err := r.FormFile("files")
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			defer file.Close()
			data, _ := io.ReadAll(file)
			if string(data) != original {
				t.Error("uploaded content differs from immutable original")
			}
			if r.FormValue("rag_provider") != "llamaindex" {
				t.Error("wrong native indexing provider")
			}
			created = true
			uploads++
			fmt.Fprint(w, `{"task_id":"build-1"}`)
		case strings.HasSuffix(r.URL.Path, "/progress"):
			fmt.Fprint(w, `{"stage":"processing_documents"}`)
		case strings.HasPrefix(r.URL.Path, "/api/knowledge-bases/"):
			if !created {
				w.WriteHeader(404)
				fmt.Fprint(w, `{}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ready", "statistics": map[string]any{"raw_documents": 1, "rag_initialized": validated, "active_match": validated, "needs_reindex": false}})
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{}`)
		}
	}))
	defer upstream.Close()
	cfg := testutil.Config(t)
	cfg.Teaching.DeepTutorURL = upstream.URL
	cfg.Teaching.DeepTutorAdminUsername = "rka-admin"
	cfg.Teaching.DeepTutorAdminPassword = "test-only"
	cfg.Teaching.EmbeddingURL = "http://embedding.invalid/v1/embeddings"
	cfg.Teaching.EmbeddingModel = "test-embedding"
	cfg.Teaching.EmbeddingDims = 2
	app, server, db := testutil.NewAppWithConfig(t, cfg)
	testutil.CreateUser(t, app, "index-admin@example.com", "correct-horse-battery", "admin")
	testutil.Login(t, server, "index-admin@example.com", "correct-horse-battery")
	sum := sha256.Sum256([]byte(original))
	sha := fmt.Sprintf("%x", sum)
	if err := os.WriteFile(filepath.Join(cfg.OriginalsDir(), "chapter.md"), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `insert into blobs(sha256,size,rel_path)values($1,$2,'chapter.md')`, sha, len(original)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `insert into materials(id,title,book,kind,module,source_file)values('native-material','中文教材','教材','textbook','database','chapter.md')`); err != nil {
		t.Fatal(err)
	}
	var version string
	if err := db.Pool.QueryRow(t.Context(), `insert into material_versions(material_id,version,sha256,index_status)values('native-material',1,$1,'ready')returning id::text`, sha).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `update materials set current_version_id=$1::uuid where id='native-material'`, version); err != nil {
		t.Fatal(err)
	}
	run := func(service *teaching.Service) {
		t.Helper()
		worked, err := service.ProcessKnowledgeIndex(t.Context())
		if err != nil || !worked {
			t.Fatalf("index step: %v %v", worked, err)
		}
	}
	state := func() string {
		t.Helper()
		code, body := server.Do(t, http.MethodGet, "/api/content/indexes", nil)
		testutil.MustStatus(t, 200, code, body)
		return body["items"].([]any)[0].(map[string]any)["status"].(string)
	}
	advance := func() {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), `update knowledge_indexes set lease_until=now()-interval '1 second'`); err != nil {
			t.Fatal(err)
		}
	}
	run(app.Teaching)
	if state() != "indexing" {
		t.Fatal("upload was incorrectly published as ready")
	}
	restarted := teaching.New(teaching.Deps{Config: cfg, DB: db, Log: testutil.Logger()})
	advance()
	run(restarted)
	if state() != "indexing" {
		t.Fatal("unverified native index was published")
	}
	mu.Lock()
	validated = true
	mu.Unlock()
	advance()
	run(restarted)
	if state() != "ready" {
		t.Fatal("validated native index was not published")
	}
	mu.Lock()
	count := uploads
	mu.Unlock()
	if count != 1 {
		t.Fatalf("restart duplicated uploads: %d", count)
	}
	// Revoked/private materials must disappear from both the catalog and turn scope.
	learner := testutil.CreateUser(t, app, "index-learner@example.com", "correct-horse-battery", "learner")
	other := testutil.NewServer(t, app.Handler)
	testutil.Login(t, other, learner.Email, "correct-horse-battery")
	if _, err := db.Pool.Exec(t.Context(), `update materials set review_status='draft' where id='native-material'`); err != nil {
		t.Fatal(err)
	}
	code, body := other.Do(t, http.MethodGet, "/api/content/indexes", nil)
	testutil.MustStatus(t, 200, code, body)
	if len(body["items"].([]any)) != 0 {
		t.Fatal("private index leaked to learner")
	}
	if _, err := app.Teaching.PrepareTurn(t.Context(), teaching.PrepareInput{UserID: learner.ID, Kind: "explain", Context: map[string]any{"materialId": "native-material"}}); err == nil {
		t.Fatal("private material accepted for teaching")
	}
}

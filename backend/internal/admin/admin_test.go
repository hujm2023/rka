package admin_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rka/internal/testutil"
)

const (
	password       = "correct-horse-battery"
	testCredential = "test-only-credential"
)

func errorCode(body map[string]any) string {
	envelope, _ := body["error"].(map[string]any)
	code, _ := envelope["code"].(string)
	return code
}

func TestAdminStatusSeparatesSignalsAndRequiresAdmin(t *testing.T) {
	application, server, _ := testutil.NewApp(t)
	testutil.CreateUser(t, application, "learner@example.com", password, "learner")
	testutil.CreateUser(t, application, "admin@example.com", password, "admin")

	testutil.Login(t, server, "learner@example.com", password)
	status, body := server.Do(t, http.MethodGet, "/api/admin/status", nil)
	testutil.MustStatus(t, http.StatusForbidden, status, body)

	// A second server has its own cookie jar; log in as admin there.
	_, adminServer, _ := testutil.NewAppFor(t, application)
	testutil.Login(t, adminServer, "admin@example.com", password)
	status, body = adminServer.Do(t, http.MethodGet, "/api/admin/status", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	for _, key := range []string{"process", "database", "teaching", "model", "index"} {
		component, ok := body[key].(map[string]any)
		if !ok {
			t.Fatalf("status missing component %q: %v", key, body)
		}
		if component["status"] == nil {
			t.Fatalf("component %q has no status", key)
		}
	}
	teaching, _ := body["teaching"].(map[string]any)
	if teaching["status"] != "unconfigured" {
		t.Fatalf("teaching = %v, want unconfigured", teaching)
	}
	if teaching["reason"] == "" || teaching["reason"] == nil {
		t.Fatalf("unconfigured teaching must explain why: %v", teaching)
	}
	model, _ := body["model"].(map[string]any)
	if model["status"] != "unconfigured" {
		t.Fatalf("model = %v, want unconfigured", model)
	}
	version, _ := body["version"].(map[string]any)
	if version["binarySchema"] == nil {
		t.Fatalf("status missing version info: %v", version)
	}
	checks, _ := body["checks"].(map[string]any)
	if checks == nil {
		t.Fatalf("status missing top-level checks: %v", body)
	}
	for _, key := range []string{"process", "database", "teaching", "model", "index"} {
		if _, ok := checks[key].(string); !ok {
			t.Fatalf("checks[%s] must be a status string: %v", key, checks[key])
		}
	}
	if body["status"] != "ok" {
		t.Fatalf("overall status = %v, want ok for ok/unconfigured components: %v", body["status"], checks)
	}
}

func TestModelConfigCredentialOnlyWriteAndRealCheck(t *testing.T) {
	var gotAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"fake-model"}]}`))
		case "/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	application, server, db := testutil.NewApp(t)
	testutil.CreateUser(t, application, "admin@example.com", password, "admin")
	testutil.Login(t, server, "admin@example.com", password)

	var configID string
	if err := db.Pool.QueryRow(t.Context(), `
		insert into model_configs (name, kind, endpoint, model, config_status, created_by)
		select '测试模型', 'llm', $1, 'fake-model', 'configured', id from users where role = 'admin' limit 1
		returning id::text`, upstream.URL).Scan(&configID); err != nil {
		t.Fatalf("insert model config: %v", err)
	}

	status, body := server.Do(t, http.MethodPut, "/api/admin/model-configs/"+configID, map[string]any{
		"credential": "test-only-credential",
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	serialized, _ := json.Marshal(body)
	if strings.Contains(string(serialized), "test-only-credential") {
		t.Fatalf("credential leaked in response: %s", string(serialized))
	}
	config, _ := body["config"].(map[string]any)
	if config["authConfigured"] != true || config["credentialPresent"] != true {
		t.Fatalf("credential state not reflected: %v", config)
	}

	var cipher, plain string
	if err := db.Pool.QueryRow(t.Context(), `
		select credential_ciphertext, credential_ciphertext from model_configs where id = $1::uuid`, configID).
		Scan(&cipher, &plain); err != nil {
		t.Fatal(err)
	}
	if cipher == "test-only-credential" || !strings.HasPrefix(cipher, "v1:") {
		t.Fatalf("credential stored without AES-GCM envelope: %q", cipher)
	}

	status, body = server.Do(t, http.MethodPost, "/api/admin/model-configs/"+configID+"/check", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	connection, _ := body["connection"].(map[string]any)
	capability, _ := body["capability"].(map[string]any)
	if connection["status"] != "ok" {
		t.Fatalf("connection status = %v (%v)", connection["status"], connection["reason"])
	}
	if capability["status"] != "verified" {
		t.Fatalf("capability status = %v (%v)", capability["status"], capability["reason"])
	}
	if gotAuth != "Bearer test-only-credential" {
		t.Fatalf("upstream did not receive decrypted credential, got %q", gotAuth)
	}

	// The list endpoint must never expose the plaintext or the ciphertext.
	status, body = server.Do(t, http.MethodGet, "/api/admin/model-configs", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	serialized, _ = json.Marshal(body)
	if strings.Contains(string(serialized), "test-only-credential") || strings.Contains(string(serialized), "v1:") {
		t.Fatalf("model config list leaked credential material")
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("model config items = %v", items)
	}
	item, _ := items[0].(map[string]any)
	if item["connectionStatus"] != "ok" || item["capabilityStatus"] != "verified" {
		t.Fatalf("stored check status not reflected: %v", item)
	}

	// Failed upstream must be recorded honestly, not reported as success.
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream down"}`))
	}))
	defer failing.Close()
	status, body = server.Do(t, http.MethodPut, "/api/admin/model-configs/"+configID, map[string]any{
		"endpoint": failing.URL,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	status, body = server.Do(t, http.MethodPost, "/api/admin/model-configs/"+configID+"/check", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	connection, _ = body["connection"].(map[string]any)
	if connection["status"] != "failed" {
		t.Fatalf("failing upstream reported as %v", connection["status"])
	}
	if connection["reason"] == "" || connection["reason"] == nil {
		t.Fatalf("failed check must record a reason")
	}
	var lastError string
	if err := db.Pool.QueryRow(t.Context(), `select last_error from model_configs where id = $1::uuid`, configID).Scan(&lastError); err != nil {
		t.Fatal(err)
	}
	if lastError == "" {
		t.Fatalf("last_error not recorded for failed check")
	}

	// Clearing the credential is a write-only operation too.
	status, body = server.Do(t, http.MethodPut, "/api/admin/model-configs/"+configID, map[string]any{
		"clearCredential": true,
	})
	testutil.MustStatus(t, http.StatusOK, status, body)
	config, _ = body["config"].(map[string]any)
	if config["authConfigured"] != false || config["credentialPresent"] != false {
		t.Fatalf("credential not cleared: %v", config)
	}

	// The update/check actions are auditable and the audit trail never
	// contains credential material.
	status, body = server.Do(t, http.MethodGet, "/api/admin/audit?action=model_config.checked", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	auditItems, _ := body["items"].([]any)
	if len(auditItems) == 0 {
		t.Fatalf("model check not audited")
	}
	serialized, _ = json.Marshal(body)
	if strings.Contains(string(serialized), "test-only-credential") || strings.Contains(string(serialized), "v1:") {
		t.Fatalf("audit trail leaked credential material")
	}
	status, body = server.Do(t, http.MethodGet, "/api/admin/audit?action=model_config.updated", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	serialized, _ = json.Marshal(body)
	if strings.Contains(string(serialized), "test-only-credential") {
		t.Fatalf("audit trail leaked credential on update")
	}
}

func TestAdminTasksAuditAndMetrics(t *testing.T) {
	application, server, db := testutil.NewApp(t)
	testutil.CreateUser(t, application, "admin@example.com", password, "admin")
	testutil.Login(t, server, "admin@example.com", password)
	var adminID string
	if err := db.Pool.QueryRow(t.Context(), `select id::text from users where role = 'admin' limit 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Pool.Exec(t.Context(), `
		insert into ai_tasks (user_id, kind, status, idempotency_key, attempt, error_code, error_message,
			created_at, started_at, finished_at)
		values ($1::uuid, 'hint', 'failed', 'task-1', 2, 'upstream_error', '上游超时',
			now() - interval '2 minutes', now() - interval '2 minutes', now() - interval '1 minute')`, adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into jobs (kind, status, errors, created_by, created_at, started_at, finished_at)
		values ('import', 'failed', '[{"message":"第 2 行解析失败"}]'::jsonb, $1::uuid,
			now() - interval '3 minutes', now() - interval '3 minutes', now() - interval '2 minutes')`, adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `
		insert into audit_log (actor_user_id, action, target_type, target_id, detail)
		values ($1::uuid, 'test.action', 'test', 't-1', '{"ok":true}'::jsonb)`, adminID); err != nil {
		t.Fatal(err)
	}

	status, body := server.Do(t, http.MethodGet, "/api/admin/tasks", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	items, _ := body["items"].([]any)
	if len(items) < 2 {
		t.Fatalf("task items = %v", items)
	}
	var aiSeen, jobSeen bool
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		switch item["source"] {
		case "ai_task":
			aiSeen = true
			if item["errorMessage"] != "上游超时" || item["retryCount"] != float64(1) {
				t.Fatalf("ai task failure details missing: %v", item)
			}
			if item["durationMs"] == float64(0) {
				t.Fatalf("ai task duration missing: %v", item)
			}
		case "job":
			jobSeen = true
			if item["errorMessage"] == "" || item["errorMessage"] == nil {
				t.Fatalf("job failure reason missing: %v", item)
			}
		}
	}
	if !aiSeen || !jobSeen {
		t.Fatalf("tasks should include both ai_task and job: %v", items)
	}

	status, body = server.Do(t, http.MethodGet, "/api/admin/audit?action=test.action", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	auditItems, _ := body["items"].([]any)
	if len(auditItems) != 1 {
		t.Fatalf("audit items = %v", auditItems)
	}
	auditItem, _ := auditItems[0].(map[string]any)
	if auditItem["detail"] == nil || auditItem["actorEmail"] != "admin@example.com" {
		t.Fatalf("audit projection incomplete: %v", auditItem)
	}

	status, body = server.Do(t, http.MethodGet, "/api/admin/metrics", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	ai, _ := body["ai"].(map[string]any)
	jobs, _ := body["jobs"].(map[string]any)
	if ai["tasksTotal"] != float64(1) || ai["failed"] != float64(1) {
		t.Fatalf("ai metrics = %v", ai)
	}
	if jobs["failed"] != float64(1) {
		t.Fatalf("job metrics = %v", jobs)
	}
	cost, _ := body["cost"].(map[string]any)
	if cost["status"] != "unknown" || cost["amount"] != nil {
		t.Fatalf("metrics must not fabricate cost: %v", cost)
	}
	reason, _ := cost["reason"].(string)
	if !strings.Contains(reason, "计费") {
		t.Fatalf("cost reason missing: %v", cost)
	}
}

func TestAdminMetricsRequiresAdmin(t *testing.T) {
	application, server, _ := testutil.NewApp(t)
	testutil.CreateUser(t, application, "learner@example.com", password, "learner")
	testutil.Login(t, server, "learner@example.com", password)
	for _, path := range []string{"/api/admin/tasks", "/api/admin/audit", "/api/admin/metrics", "/api/admin/model-configs"} {
		status, body := server.Do(t, http.MethodGet, path, nil)
		testutil.MustStatus(t, http.StatusForbidden, status, body)
	}
}

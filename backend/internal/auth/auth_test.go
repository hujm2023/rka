package auth_test

import (
	"net/http"
	"testing"

	"rka/internal/testutil"
)

const password = "correct-horse-battery"

func TestLoginMeLogout(t *testing.T) {
	application, server, _ := testutil.NewApp(t)
	testutil.CreateUser(t, application, "learner@example.com", password, "learner")

	status, body := server.Do(t, http.MethodGet, "/api/auth/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["user"] != nil {
		t.Fatalf("anonymous /me returned a user: %v", body)
	}

	status, body = server.Do(t, http.MethodPost, "/api/auth/login", map[string]any{
		"email": "learner@example.com", "password": "wrong-password",
	})
	testutil.MustStatus(t, http.StatusUnauthorized, status, body)
	if code := errorCode(body); code != "invalid_credentials" {
		t.Fatalf("error code = %q, want invalid_credentials", code)
	}

	testutil.Login(t, server, "learner@example.com", password)
	status, body = server.Do(t, http.MethodGet, "/api/auth/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	user, _ := body["user"].(map[string]any)
	if user["email"] != "learner@example.com" || user["role"] != "learner" {
		t.Fatalf("unexpected /me payload: %v", body)
	}

	status, body = server.Do(t, http.MethodGet, "/api/auth/sessions", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	sessions, _ := body["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1: %v", len(sessions), body)
	}
	first, _ := sessions[0].(map[string]any)
	if first["current"] != true {
		t.Fatalf("session should be marked current: %v", first)
	}

	status, body = server.Do(t, http.MethodPost, "/api/auth/logout", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	status, body = server.Do(t, http.MethodGet, "/api/auth/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["user"] != nil {
		t.Fatalf("logout left a user session: %v", body)
	}
}

func TestBootstrapCreatesOneAdminAndCloses(t *testing.T) {
	cfg := testutil.Config(t)
	cfg.BootstrapToken = "one-time-bootstrap-token-value"
	application, server, _ := testutil.NewAppWithConfig(t, cfg)
	_ = application

	payload := map[string]any{
		"token": "one-time-bootstrap-token-value", "email": "admin@example.com",
		"password": password, "displayName": "平台管理员",
	}
	status, body := server.Do(t, http.MethodPost, "/api/auth/bootstrap", payload)
	testutil.MustStatus(t, http.StatusCreated, status, body)
	user, _ := body["user"].(map[string]any)
	if user["role"] != "admin" {
		t.Fatalf("bootstrap user role = %v, want admin", user["role"])
	}

	// The entry point is closed after the first administrator exists.
	payload["email"] = "second-admin@example.com"
	status, body = server.Do(t, http.MethodPost, "/api/auth/bootstrap", payload)
	testutil.MustStatus(t, http.StatusConflict, status, body)
	if code := errorCode(body); code != "bootstrap_closed" {
		t.Fatalf("error code = %q, want bootstrap_closed", code)
	}

	// A wrong token is rejected before any account is created.
	cfg2 := testutil.Config(t)
	cfg2.BootstrapToken = "another-token-value"
	_, server2, _ := testutil.NewAppWithConfig(t, cfg2)
	status, body = server2.Do(t, http.MethodPost, "/api/auth/bootstrap", map[string]any{
		"token": "wrong", "email": "x@example.com", "password": password,
	})
	testutil.MustStatus(t, http.StatusForbidden, status, body)
}

func TestAdminRoleChangesApplyImmediately(t *testing.T) {
	application, adminServer, _ := testutil.NewApp(t)
	admin := testutil.CreateUser(t, application, "admin@example.com", password, "admin")
	learner := testutil.CreateUser(t, application, "learner@example.com", password, "learner")
	testutil.Login(t, adminServer, admin.Email, password)

	_, learnerServer, _ := testutil.NewAppFor(t, application)

	// A learner cannot reach admin routes.
	testutil.Login(t, learnerServer, learner.Email, password)
	status, body := learnerServer.Do(t, http.MethodGet, "/api/admin/users", nil)
	testutil.MustStatus(t, http.StatusForbidden, status, body)

	// Promote: the very next request from the existing session is authorised.
	status, body = adminServer.Do(t, http.MethodPatch, "/api/admin/users/"+learner.ID, map[string]any{"role": "admin"})
	testutil.MustStatus(t, http.StatusOK, status, body)
	status, body = learnerServer.Do(t, http.MethodGet, "/api/admin/users", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)

	// The last administrator cannot be demoted.
	status, body = adminServer.Do(t, http.MethodPatch, "/api/admin/users/"+admin.ID, map[string]any{"role": "learner"})
	testutil.MustStatus(t, http.StatusOK, status, body)
	status, body = learnerServer.Do(t, http.MethodPatch, "/api/admin/users/"+learner.ID, map[string]any{"role": "learner"})
	testutil.MustStatus(t, http.StatusConflict, status, body)
	if code := errorCode(body); code != "last_admin" {
		t.Fatalf("error code = %q, want last_admin", code)
	}
}

func TestDisabledAccountSessionsAreRevoked(t *testing.T) {
	application, adminServer, _ := testutil.NewApp(t)
	admin := testutil.CreateUser(t, application, "admin@example.com", password, "admin")
	learner := testutil.CreateUser(t, application, "learner@example.com", password, "learner")
	testutil.Login(t, adminServer, admin.Email, password)
	_, learnerServer, _ := testutil.NewAppFor(t, application)
	testutil.Login(t, learnerServer, learner.Email, password)

	status, body := learnerServer.Do(t, http.MethodGet, "/api/auth/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["user"] == nil {
		t.Fatalf("learner should be signed in: %v", body)
	}

	status, body = adminServer.Do(t, http.MethodPatch, "/api/admin/users/"+learner.ID, map[string]any{"status": "disabled"})
	testutil.MustStatus(t, http.StatusOK, status, body)

	status, body = learnerServer.Do(t, http.MethodGet, "/api/auth/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["user"] != nil {
		t.Fatalf("disabled account kept a session: %v", body)
	}
	status, body = learnerServer.Do(t, http.MethodPost, "/api/auth/login", map[string]any{
		"email": learner.Email, "password": password,
	})
	testutil.MustStatus(t, http.StatusForbidden, status, body)
	if code := errorCode(body); code != "account_disabled" {
		t.Fatalf("error code = %q, want account_disabled", code)
	}
}

func TestInviteAcceptFlow(t *testing.T) {
	application, adminServer, _ := testutil.NewApp(t)
	admin := testutil.CreateUser(t, application, "admin@example.com", password, "admin")
	testutil.Login(t, adminServer, admin.Email, password)

	status, body := adminServer.Do(t, http.MethodPost, "/api/auth/invites", map[string]any{
		"email": "invited@example.com", "role": "learner",
	})
	testutil.MustStatus(t, http.StatusCreated, status, body)
	rawToken, _ := body["token"].(string)
	if rawToken == "" {
		t.Fatalf("invite returned no token: %v", body)
	}

	_, guestServer, _ := testutil.NewAppFor(t, application)
	status, body = guestServer.Do(t, http.MethodPost, "/api/auth/invites/accept", map[string]any{
		"token": rawToken, "password": password, "displayName": "被邀请学习者",
	})
	testutil.MustStatus(t, http.StatusCreated, status, body)
	user, _ := body["user"].(map[string]any)
	if user["email"] != "invited@example.com" {
		t.Fatalf("unexpected invited user: %v", user)
	}

	// Re-using the invite is rejected.
	_, guest2, _ := testutil.NewAppFor(t, application)
	status, body = guest2.Do(t, http.MethodPost, "/api/auth/invites/accept", map[string]any{
		"token": rawToken, "password": password,
	})
	testutil.MustStatus(t, http.StatusBadRequest, status, body)
}

func TestPasswordChangeRevokesOtherSessions(t *testing.T) {
	application, first, _ := testutil.NewApp(t)
	user := testutil.CreateUser(t, application, "learner@example.com", password, "learner")
	testutil.Login(t, first, user.Email, password)
	_, second, _ := testutil.NewAppFor(t, application)
	testutil.Login(t, second, user.Email, password)

	status, body := first.Do(t, http.MethodPost, "/api/auth/password", map[string]any{
		"currentPassword": password, "newPassword": "a-brand-new-password",
	})
	testutil.MustStatus(t, http.StatusOK, status, body)

	status, body = first.Do(t, http.MethodGet, "/api/auth/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["user"] == nil {
		t.Fatalf("current session should stay signed in: %v", body)
	}
	status, body = second.Do(t, http.MethodGet, "/api/auth/me", nil)
	testutil.MustStatus(t, http.StatusOK, status, body)
	if body["user"] != nil {
		t.Fatalf("other session should be revoked: %v", body)
	}
}

func TestSameOriginGuardRejectsCrossSiteWrites(t *testing.T) {
	_, server, _ := testutil.NewApp(t)
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/auth/login", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", "https://evil.example")
	response, err := server.Client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin write status = %d, want 403", response.StatusCode)
	}
}

func errorCode(body map[string]any) string {
	errBody, _ := body["error"].(map[string]any)
	code, _ := errBody["code"].(string)
	return code
}

package auth

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"rka/internal/audit"
	"rka/internal/httpx"
	"rka/internal/store"
	"rka/internal/token"
)

// Register installs every identity, session, invite and grant route.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", s.Require(s.handleLogout))
	mux.HandleFunc("GET /api/auth/me", s.handleMe)
	mux.HandleFunc("POST /api/auth/password", s.Require(s.handleChangePassword))
	mux.HandleFunc("GET /api/auth/sessions", s.Require(s.handleListSessions))
	mux.HandleFunc("DELETE /api/auth/sessions/{id}", s.Require(s.handleRevokeSession))
	mux.HandleFunc("POST /api/auth/bootstrap", s.handleBootstrap)
	mux.HandleFunc("POST /api/auth/invites", s.RequireAdmin(s.handleCreateInvite))
	mux.HandleFunc("POST /api/auth/invites/accept", s.handleAcceptInvite)

	mux.HandleFunc("GET /api/admin/users", s.RequireAdmin(s.handleListUsers))
	mux.HandleFunc("POST /api/admin/users", s.RequireAdmin(s.handleCreateUser))
	mux.HandleFunc("PATCH /api/admin/users/{id}", s.RequireAdmin(s.handlePatchUser))

	mux.HandleFunc("GET /api/admin/grants", s.RequireAdmin(s.handleListGrants))
	mux.HandleFunc("POST /api/admin/grants", s.RequireAdmin(s.handleCreateGrant))
	mux.HandleFunc("DELETE /api/admin/grants/{id}", s.RequireAdmin(s.handleRevokeGrant))
}

func (s *Service) record(r *http.Request, action, targetType, targetID string, detail map[string]any) {
	s.audit.Record(r.Context(), audit.Entry{
		ActorID:    httpx.ActorFrom(r.Context()),
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Detail:     detail,
		RequestID:  httpx.RequestIDFrom(r.Context()),
		IP:         httpx.ClientIP(r, s.cfg.TrustProxy),
	})
}

// ---------------------------------------------------------------- login

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.Decode(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	email := normalizeEmail(req.Email)
	if email == "" || req.Password == "" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "missing_fields", "请输入邮箱和密码。"))
		return
	}
	key := httpx.ClientIP(r, s.cfg.TrustProxy) + "|" + email
	if blocked, wait := s.limiter.blocked(key, time.Now()); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		httpx.Error(w, r, httpx.NewError(http.StatusTooManyRequests, "rate_limited", "登录尝试过于频繁，请稍后再试。"))
		return
	}
	user, hash, err := s.FindUserByEmail(r.Context(), email)
	if err != nil {
		// Burn comparable time so a missing account is not detectable.
		_ = VerifyPassword(s.dummy, req.Password)
		s.limiter.fail(key, time.Now())
		s.record(r, "login_failed", "user", "", map[string]any{"email": email, "reason": "unknown_account"})
		httpx.Error(w, r, errInvalidCredentials)
		return
	}
	if !VerifyPassword(hash, req.Password) {
		s.limiter.fail(key, time.Now())
		s.record(r, "login_failed", "user", user.ID, map[string]any{"reason": "wrong_password"})
		httpx.Error(w, r, errInvalidCredentials)
		return
	}
	if user.Status != "active" {
		httpx.Error(w, r, errDisabled)
		return
	}
	s.limiter.reset(key)
	expiresAt, err := s.createSession(r.Context(), w, r, user.ID)
	if err != nil {
		s.log.Error("create session", "error", err)
		httpx.Error(w, r, httpx.NewError(http.StatusInternalServerError, "internal", "登录失败，请重试。"))
		return
	}
	_, _ = s.db.Pool.Exec(r.Context(), `update users set last_login_at = now(), updated_at = now() where id = $1::uuid`, user.ID)
	s.record(r, "login", "user", user.ID, map[string]any{"ip": httpx.ClientIP(r, s.cfg.TrustProxy)})
	httpx.JSON(w, r, http.StatusOK, map[string]any{"user": user, "expiresAt": expiresAt})
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())
	if id := SessionID(r.Context()); id != "" {
		_, _ = s.db.Pool.Exec(r.Context(), `update sessions set revoked_at = now() where id = $1::uuid`, id)
	}
	s.clearSessionCookie(w, r)
	s.record(r, "logout", "user", user.ID, nil)
	httpx.JSON(w, r, http.StatusOK, map[string]any{"ok": true})
}

func (s *Service) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := UserFrom(r.Context())
	if !ok {
		httpx.JSON(w, r, http.StatusOK, map[string]any{"user": nil})
		return
	}
	info, _ := r.Context().Value(sessionInfoKey).(sessionInfo)
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"user":         user,
		"capabilities": map[string]any{"admin": user.IsAdmin()},
		"session":      map[string]any{"id": info.ID, "expiresAt": info.ExpiresAt},
	})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (s *Service) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	var req changePasswordRequest
	if err := httpx.Decode(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	user := MustUser(r.Context())
	_, hash, err := s.FindUserByEmail(r.Context(), user.Email)
	if err != nil {
		httpx.Error(w, r, httpx.ErrUnauthorized)
		return
	}
	if !VerifyPassword(hash, req.CurrentPassword) {
		httpx.Error(w, r, httpx.NewError(http.StatusForbidden, "wrong_password", "当前密码不正确。"))
		return
	}
	if err := ValidatePassword(req.NewPassword); err != nil {
		httpx.Error(w, r, err)
		return
	}
	next, err := HashPassword(req.NewPassword)
	if err != nil {
		httpx.Error(w, r, httpx.ErrUnavailable)
		return
	}
	currentSession := SessionID(r.Context())
	if _, err := s.db.Pool.Exec(r.Context(), `
		update users set password_hash = $2, must_change_password = false, updated_at = now() where id = $1::uuid`,
		user.ID, next); err != nil {
		httpx.Error(w, r, err)
		return
	}
	// Other devices must sign in again; the current one stays usable.
	_, _ = s.db.Pool.Exec(r.Context(), `
		update sessions set revoked_at = now()
		where user_id = $1::uuid and revoked_at is null and id <> $2::uuid`, user.ID, currentSession)
	s.record(r, "password_changed", "user", user.ID, nil)
	httpx.JSON(w, r, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- devices

func (s *Service) handleListSessions(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())
	current := SessionID(r.Context())
	rows, err := s.db.Pool.Query(r.Context(), `
		select id::text, user_agent, ip, created_at, last_seen_at, expires_at
		from sessions
		where user_id = $1::uuid and revoked_at is null and expires_at > now()
		order by last_seen_at desc`, user.ID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	defer rows.Close()
	type device struct {
		ID         string    `json:"id"`
		UserAgent  string    `json:"userAgent"`
		IP         string    `json:"ip"`
		CreatedAt  time.Time `json:"createdAt"`
		LastSeenAt time.Time `json:"lastSeenAt"`
		ExpiresAt  time.Time `json:"expiresAt"`
		Current    bool      `json:"current"`
	}
	devices := []device{}
	for rows.Next() {
		var d device
		if err := rows.Scan(&d.ID, &d.UserAgent, &d.IP, &d.CreatedAt, &d.LastSeenAt, &d.ExpiresAt); err != nil {
			httpx.Error(w, r, err)
			return
		}
		d.Current = d.ID == current
		devices = append(devices, d)
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"sessions": devices})
}

func (s *Service) handleRevokeSession(w http.ResponseWriter, r *http.Request) {
	user := MustUser(r.Context())
	id := r.PathValue("id")
	tag, err := s.db.Pool.Exec(r.Context(), `
		update sessions set revoked_at = now()
		where id = $1::uuid and user_id = $2::uuid and revoked_at is null`, id, user.ID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, r, httpx.ErrNotFound)
		return
	}
	s.record(r, "session_revoked", "session", id, nil)
	if id == SessionID(r.Context()) {
		s.clearSessionCookie(w, r)
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- bootstrap

type bootstrapRequest struct {
	Token       string `json:"token"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

// handleBootstrap creates the first administrator through a controlled,
// one-time entry point. It closes itself after success.
func (s *Service) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	var req bootstrapRequest
	if err := httpx.Decode(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if strings.TrimSpace(s.cfg.BootstrapToken) == "" {
		httpx.Error(w, r, httpx.NewError(http.StatusForbidden, "bootstrap_disabled", "未配置一次性初始化令牌，无法建立首个管理员。"))
		return
	}
	if !token.Equal(token.Hash(strings.TrimSpace(req.Token)), token.Hash(strings.TrimSpace(s.cfg.BootstrapToken))) {
		s.record(r, "bootstrap_denied", "platform", "", map[string]any{"reason": "bad_token"})
		httpx.Error(w, r, httpx.NewError(http.StatusForbidden, "bad_token", "初始化令牌不正确。"))
		return
	}
	if err := validateEmail(req.Email); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := ValidatePassword(req.Password); err != nil {
		httpx.Error(w, r, err)
		return
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		httpx.Error(w, r, httpx.ErrUnavailable)
		return
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = strings.SplitN(normalizeEmail(req.Email), "@", 2)[0]
	}
	var userID string
	err = s.db.Tx(r.Context(), func(tx pgxTx) error {
		var bootstrapped *time.Time
		var closed bool
		if err := tx.QueryRow(r.Context(), `select admin_bootstrapped_at, bootstrap_closed from platform_state where id = 1 for update`).
			Scan(&bootstrapped, &closed); err != nil {
			return err
		}
		if closed || bootstrapped != nil {
			return errBootstrapClosed
		}
		var admins int
		if err := tx.QueryRow(r.Context(), `select count(*) from users where role = 'admin'`).Scan(&admins); err != nil {
			return err
		}
		if admins > 0 {
			return errBootstrapClosed
		}
		if err := tx.QueryRow(r.Context(), `
			insert into users (email, display_name, role, password_hash)
			values (lower($1), $2, 'admin', $3)
			returning id::text`, normalizeEmail(req.Email), displayName, hash).Scan(&userID); err != nil {
			return err
		}
		_, err := tx.Exec(r.Context(), `
			update platform_state set admin_bootstrapped_at = now(), bootstrap_closed = true, updated_at = now() where id = 1`)
		return err
	})
	if err != nil {
		if err == errBootstrapClosed {
			httpx.Error(w, r, errBootstrapClosed)
			return
		}
		if store.IsUniqueViolation(err) {
			httpx.Error(w, r, httpx.NewError(http.StatusConflict, "email_taken", "该邮箱已被使用。"))
			return
		}
		s.log.Error("bootstrap", "error", err)
		httpx.Error(w, r, httpx.ErrUnavailable)
		return
	}
	user, err := s.FindUser(r.Context(), userID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	expiresAt, err := s.createSession(r.Context(), w, r, user.ID)
	if err != nil {
		httpx.Error(w, r, httpx.ErrUnavailable)
		return
	}
	s.record(r, "admin_bootstrapped", "user", user.ID, nil)
	httpx.JSON(w, r, http.StatusCreated, map[string]any{"user": user, "expiresAt": expiresAt})
}

// ---------------------------------------------------------------- invites

type inviteRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (s *Service) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	var req inviteRequest
	if err := httpx.Decode(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := validateEmail(req.Email); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if req.Role == "" {
		req.Role = "learner"
	}
	if req.Role != "admin" && req.Role != "learner" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_role", "角色只能是 admin 或 learner。"))
		return
	}
	raw, hash, err := token.New(32)
	if err != nil {
		httpx.Error(w, r, httpx.ErrUnavailable)
		return
	}
	var inviteID string
	var expiresAt time.Time
	err = s.db.Pool.QueryRow(r.Context(), `
		insert into invites (email, role, token_hash, created_by, expires_at)
		values (lower($1), $2, $3, $4::uuid, now() + interval '7 days')
		returning id::text, expires_at`,
		normalizeEmail(req.Email), req.Role, hash, MustUser(r.Context()).ID).Scan(&inviteID, &expiresAt)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.record(r, "invite_created", "invite", inviteID, map[string]any{"email": normalizeEmail(req.Email), "role": req.Role})
	httpx.JSON(w, r, http.StatusCreated, map[string]any{
		"id":        inviteID,
		"email":     normalizeEmail(req.Email),
		"role":      req.Role,
		"token":     raw,
		"expiresAt": expiresAt,
	})
}

type acceptInviteRequest struct {
	Token       string `json:"token"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

func (s *Service) handleAcceptInvite(w http.ResponseWriter, r *http.Request) {
	var req acceptInviteRequest
	if err := httpx.Decode(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := ValidatePassword(req.Password); err != nil {
		httpx.Error(w, r, err)
		return
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		httpx.Error(w, r, httpx.ErrUnavailable)
		return
	}
	var (
		userID   string
		inviteID string
		email    string
		role     string
	)
	err = s.db.Tx(r.Context(), func(tx pgxTx) error {
		var displayName string
		if err := tx.QueryRow(r.Context(), `
			select id::text, email, role from invites
			where token_hash = $1 and accepted_at is null and expires_at > now()
			for update`, token.Hash(strings.TrimSpace(req.Token))).Scan(&inviteID, &email, &role); err != nil {
			return errInviteInvalid
		}
		displayName = strings.TrimSpace(req.DisplayName)
		if displayName == "" {
			displayName = strings.SplitN(email, "@", 2)[0]
		}
		if err := tx.QueryRow(r.Context(), `
			insert into users (email, display_name, role, password_hash)
			values (lower($1), $2, $3, $4)
			returning id::text`, email, displayName, role, hash).Scan(&userID); err != nil {
			return err
		}
		_, err := tx.Exec(r.Context(), `update invites set accepted_at = now(), accepted_by = $1::uuid where id = $2::uuid`, userID, inviteID)
		return err
	})
	if err != nil {
		if err == errInviteInvalid {
			httpx.Error(w, r, errInviteInvalid)
			return
		}
		if store.IsUniqueViolation(err) {
			httpx.Error(w, r, httpx.NewError(http.StatusConflict, "email_taken", "该邮箱已注册，请直接登录。"))
			return
		}
		httpx.Error(w, r, err)
		return
	}
	user, err := s.FindUser(r.Context(), userID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	expiresAt, err := s.createSession(r.Context(), w, r, user.ID)
	if err != nil {
		httpx.Error(w, r, httpx.ErrUnavailable)
		return
	}
	s.record(r, "invite_accepted", "user", user.ID, map[string]any{"invite": inviteID})
	httpx.JSON(w, r, http.StatusCreated, map[string]any{"user": user, "expiresAt": expiresAt})
}

// ---------------------------------------------------------------- admin users

func (s *Service) handleListUsers(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	rows, err := s.db.Pool.Query(r.Context(), `
		select id::text, email, display_name, role, status, must_change_password, created_at, last_login_at
		from users
		where $1 = '' or lower(email) like '%' || lower($1) || '%' or lower(display_name) like '%' || lower($1) || '%'
		order by created_at`, query)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.Status,
			&user.MustChangePassword, &user.CreatedAt, &user.LastLoginAt); err != nil {
			httpx.Error(w, r, err)
			return
		}
		users = append(users, user)
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"users": users})
}

type createUserRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Role        string `json:"role"`
	Password    string `json:"password"`
}

func (s *Service) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := httpx.Decode(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := validateEmail(req.Email); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := ValidatePassword(req.Password); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if req.Role == "" {
		req.Role = "learner"
	}
	if req.Role != "admin" && req.Role != "learner" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_role", "角色只能是 admin 或 learner。"))
		return
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = strings.SplitN(normalizeEmail(req.Email), "@", 2)[0]
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		httpx.Error(w, r, httpx.ErrUnavailable)
		return
	}
	var id string
	err = s.db.Pool.QueryRow(r.Context(), `
		insert into users (email, display_name, role, password_hash, must_change_password)
		values (lower($1), $2, $3, $4, true) returning id::text`,
		normalizeEmail(req.Email), displayName, req.Role, hash).Scan(&id)
	if err != nil {
		if store.IsUniqueViolation(err) {
			httpx.Error(w, r, httpx.NewError(http.StatusConflict, "email_taken", "该邮箱已被使用。"))
			return
		}
		httpx.Error(w, r, err)
		return
	}
	user, _ := s.FindUser(r.Context(), id)
	s.record(r, "user_created", "user", id, map[string]any{"role": req.Role})
	httpx.JSON(w, r, http.StatusCreated, map[string]any{"user": user})
}

type patchUserRequest struct {
	DisplayName *string `json:"displayName"`
	Role        *string `json:"role"`
	Status      *string `json:"status"`
	NewPassword *string `json:"newPassword"`
}

func (s *Service) handlePatchUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req patchUserRequest
	if err := httpx.Decode(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor := MustUser(r.Context())
	target, err := s.FindUser(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, httpx.ErrNotFound)
		return
	}
	if req.Role != nil {
		if *req.Role != "admin" && *req.Role != "learner" {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_role", "角色只能是 admin 或 learner。"))
			return
		}
	}
	if req.Status != nil && *req.Status != "active" && *req.Status != "disabled" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_status", "状态只能是 active 或 disabled。"))
		return
	}
	if req.Status != nil && *req.Status == "disabled" && target.ID == actor.ID {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "self_disable", "不能停用当前登录的管理员账号。"))
		return
	}
	var newHash *string
	if req.NewPassword != nil {
		if err := ValidatePassword(*req.NewPassword); err != nil {
			httpx.Error(w, r, err)
			return
		}
		hash, err := HashPassword(*req.NewPassword)
		if err != nil {
			httpx.Error(w, r, httpx.ErrUnavailable)
			return
		}
		newHash = &hash
	}
	err = s.db.Tx(r.Context(), func(tx pgxTx) error {
		if _, err := tx.Exec(r.Context(), `select pg_advisory_xact_lock(hashtextextended('rka-admin-membership', 0))`); err != nil {
			return err
		}
		var authorized bool
		if err := tx.QueryRow(r.Context(), `select role='admin' and status='active' from users where id=$1::uuid`, actor.ID).Scan(&authorized); err != nil {
			return err
		}
		if !authorized {
			return httpx.NewError(http.StatusForbidden, "admin_required", "当前账号已没有管理权限。")
		}
		if _, err := tx.Exec(r.Context(), `
			update users set
				display_name = coalesce($2, display_name),
				role = coalesce($3, role),
				status = coalesce($4, status),
				password_hash = coalesce($5, password_hash),
				must_change_password = case when $5 is null then must_change_password else false end,
				updated_at = now()
			where id = $1::uuid`,
			target.ID, req.DisplayName, req.Role, req.Status, newHash); err != nil {
			return err
		}
		var admins int
		if err := tx.QueryRow(r.Context(), `select count(*) from users where role='admin' and status='active'`).Scan(&admins); err != nil {
			return err
		}
		if admins == 0 {
			return httpx.NewError(http.StatusConflict, "last_admin", "平台至少需要保留一个可用的管理员。")
		}
		if req.Status != nil && *req.Status == "disabled" {
			if _, err := tx.Exec(r.Context(), `
				update sessions set revoked_at = now()
				where user_id = $1::uuid and revoked_at is null`, target.ID); err != nil {
				return err
			}
		}
		if newHash != nil {
			if _, err := tx.Exec(r.Context(), `
				update sessions set revoked_at = now()
				where user_id = $1::uuid and revoked_at is null and id <> $2::uuid`, target.ID, SessionID(r.Context())); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	updated, err := s.FindUser(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.record(r, "user_updated", "user", id, map[string]any{
		"role": req.Role, "status": req.Status, "passwordReset": newHash != nil,
	})
	httpx.JSON(w, r, http.StatusOK, map[string]any{"user": updated})
}

// ---------------------------------------------------------------- grants

func (s *Service) handleListGrants(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Pool.Query(r.Context(), `
		select g.id::text, g.user_id::text, u.email, g.resource_type, g.resource_id, g.created_at
		from content_grants g join users u on u.id = g.user_id
		where g.revoked_at is null
		order by g.created_at desc`)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	defer rows.Close()
	type grant struct {
		ID           string    `json:"id"`
		UserID       string    `json:"userId"`
		Email        string    `json:"email"`
		ResourceType string    `json:"resourceType"`
		ResourceID   string    `json:"resourceId"`
		CreatedAt    time.Time `json:"createdAt"`
	}
	grants := []grant{}
	for rows.Next() {
		var g grant
		if err := rows.Scan(&g.ID, &g.UserID, &g.Email, &g.ResourceType, &g.ResourceID, &g.CreatedAt); err != nil {
			httpx.Error(w, r, err)
			return
		}
		grants = append(grants, g)
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"grants": grants})
}

type grantRequest struct {
	UserID       string `json:"userId"`
	ResourceType string `json:"resourceType"`
	ResourceID   string `json:"resourceId"`
}

func (s *Service) handleCreateGrant(w http.ResponseWriter, r *http.Request) {
	var req grantRequest
	if err := httpx.Decode(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	switch req.ResourceType {
	case "material", "collection", "capability":
	default:
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_grant", "授权类型只能是 material、collection 或 capability。"))
		return
	}
	if strings.TrimSpace(req.ResourceID) == "" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_grant", "缺少资源标识。"))
		return
	}
	var id string
	err := s.db.Pool.QueryRow(r.Context(), `
		insert into content_grants (user_id, resource_type, resource_id, granted_by)
		values ($1::uuid, $2, $3, $4::uuid)
		on conflict (user_id, resource_type, resource_id)
		do update set revoked_at = null, granted_by = excluded.granted_by
		returning id::text`, req.UserID, req.ResourceType, req.ResourceID, MustUser(r.Context()).ID).Scan(&id)
	if err != nil {
		if store.IsForeignKeyViolation(err) {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "unknown_user", "指定的用户不存在。"))
			return
		}
		httpx.Error(w, r, err)
		return
	}
	s.record(r, "grant_created", req.ResourceType, req.ResourceID, map[string]any{"userId": req.UserID})
	httpx.JSON(w, r, http.StatusCreated, map[string]any{"id": id})
}

func (s *Service) handleRevokeGrant(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	tag, err := s.db.Pool.Exec(r.Context(), `update content_grants set revoked_at = now() where id = $1::uuid and revoked_at is null`, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, r, httpx.ErrNotFound)
		return
	}
	s.record(r, "grant_revoked", "grant", id, nil)
	httpx.JSON(w, r, http.StatusOK, map[string]any{"ok": true})
}

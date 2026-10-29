// Package auth owns identities, password credentials, login sessions, device
// sessions, invites, roles and content grants.
//
// RKA is the authority for identity: a teaching service outage must never block
// login, reading or practice.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"

	"rka/internal/audit"
	"rka/internal/config"
	"rka/internal/httpx"
	"rka/internal/store"
	"rka/internal/token"
)

type User struct {
	ID                 string     `json:"id"`
	Email              string     `json:"email"`
	DisplayName        string     `json:"displayName"`
	Role               string     `json:"role"`
	Status             string     `json:"status"`
	MustChangePassword bool       `json:"mustChangePassword"`
	CreatedAt          time.Time  `json:"createdAt"`
	LastLoginAt        *time.Time `json:"lastLoginAt,omitempty"`
}

func (u User) IsAdmin() bool { return u.Role == "admin" }

type sessionInfo struct {
	ID        string
	ExpiresAt time.Time
}

type ctxKey int

const ctxUser ctxKey = iota

type Service struct {
	cfg     *config.Config
	db      *store.DB
	log     *slog.Logger
	audit   *audit.Recorder
	limiter *limiter
	dummy   string
}

func New(cfg *config.Config, db *store.DB, log *slog.Logger, recorder *audit.Recorder) *Service {
	service := &Service{cfg: cfg, db: db, log: log, audit: recorder, limiter: newLimiter()}
	// A precomputed hash keeps a missing account indistinguishable from a wrong
	// password by response time.
	service.dummy, _ = HashPassword("rka-dummy-password-value")
	return service
}

// ---------------------------------------------------------------- passwords

const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

// HashPassword returns an argon2id PHC string.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks a password against a PHC string.
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var memory uint32
	var timeCost uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, timeCost, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ValidatePassword enforces the minimum account policy.
func ValidatePassword(password string) error {
	if len(password) < 10 {
		return httpx.NewError(http.StatusBadRequest, "weak_password", "密码至少需要 10 个字符。")
	}
	if len(password) > 200 {
		return httpx.NewError(http.StatusBadRequest, "weak_password", "密码过长。")
	}
	if strings.TrimSpace(password) == "" {
		return httpx.NewError(http.StatusBadRequest, "weak_password", "密码不能只包含空白字符。")
	}
	return nil
}

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func validateEmail(email string) error {
	email = normalizeEmail(email)
	if len(email) < 5 || len(email) > 254 || strings.Count(email, "@") != 1 {
		return httpx.NewError(http.StatusBadRequest, "invalid_email", "请输入有效的邮箱地址。")
	}
	parts := strings.SplitN(email, "@", 2)
	if parts[0] == "" || !strings.Contains(parts[1], ".") || strings.HasPrefix(parts[1], ".") || strings.HasSuffix(parts[1], ".") {
		return httpx.NewError(http.StatusBadRequest, "invalid_email", "请输入有效的邮箱地址。")
	}
	return nil
}

// ---------------------------------------------------------------- limiter

type limiter struct {
	mu      sync.Mutex
	entries map[string]*limitEntry
}

type limitEntry struct {
	failures int
	resetAt  time.Time
}

func newLimiter() *limiter { return &limiter{entries: map[string]*limitEntry{}} }

const (
	limitFailures = 10
	limitWindow   = 15 * time.Minute
)

func (l *limiter) blocked(key string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[key]
	if !ok {
		return false, 0
	}
	if now.After(entry.resetAt) {
		delete(l.entries, key)
		return false, 0
	}
	if entry.failures >= limitFailures {
		return true, time.Until(entry.resetAt)
	}
	return false, 0
}

func (l *limiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.entries[key]
	if !ok || now.After(entry.resetAt) {
		entry = &limitEntry{resetAt: now.Add(limitWindow)}
		l.entries[key] = entry
	}
	entry.failures++
	if len(l.entries) > 10_000 {
		for k, v := range l.entries {
			if now.After(v.resetAt) {
				delete(l.entries, k)
			}
		}
	}
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// ---------------------------------------------------------------- sessions

func (s *Service) cookieSecure(r *http.Request) bool {
	switch s.cfg.CookieSecure {
	case "always":
		return true
	case "never":
		return false
	}
	if r.TLS != nil {
		return true
	}
	if s.cfg.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return false
}

func (s *Service) setSessionCookie(w http.ResponseWriter, r *http.Request, raw string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.CookieName,
		Value:    raw,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
	})
}

func (s *Service) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cfg.CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

func (s *Service) createSession(ctx context.Context, w http.ResponseWriter, r *http.Request, userID string) (time.Time, error) {
	raw, hash, err := token.New(32)
	if err != nil {
		return time.Time{}, err
	}
	var sessionID string
	var dbExpires time.Time
	err = s.db.Pool.QueryRow(ctx, `
		insert into sessions (user_id, token_hash, user_agent, ip, expires_at)
		values ($1::uuid, $2, $3, $4, now() + make_interval(secs => $5))
		returning id::text, expires_at`,
		userID, hash, truncate(r.UserAgent(), 300), httpx.ClientIP(r, s.cfg.TrustProxy), int(s.cfg.SessionTTL.Seconds())).
		Scan(&sessionID, &dbExpires)
	if err != nil {
		return time.Time{}, fmt.Errorf("create session: %w", err)
	}
	_ = sessionID
	s.setSessionCookie(w, r, raw, dbExpires)
	return dbExpires, nil
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}

// sessionToken extracts the raw cookie value.
func (s *Service) sessionToken(r *http.Request) string {
	cookie, err := r.Cookie(s.cfg.CookieName)
	if err != nil || cookie.Value == "" {
		return ""
	}
	return cookie.Value
}

// Authenticate resolves the cookie session into a user. It never fails the
// request: an absent or invalid session simply leaves the request anonymous.
func (s *Service) Authenticate(ctx context.Context, r *http.Request) (context.Context, bool) {
	raw := s.sessionToken(r)
	if raw == "" {
		return ctx, false
	}
	hash := token.Hash(raw)
	var (
		sessionID string
		expiresAt time.Time
		lastSeen  time.Time
		user      User
		createdAt time.Time
		lastLogin *time.Time
	)
	err := s.db.Pool.QueryRow(ctx, `
		select s.id::text, s.expires_at, s.last_seen_at,
		       u.id::text, u.email, u.display_name, u.role, u.status,
		       u.must_change_password, u.created_at, u.last_login_at
		from sessions s
		join users u on u.id = s.user_id
		where s.token_hash = $1 and s.revoked_at is null and s.expires_at > now()`,
		hash).Scan(&sessionID, &expiresAt, &lastSeen, &user.ID, &user.Email, &user.DisplayName,
		&user.Role, &user.Status, &user.MustChangePassword, &createdAt, &lastLogin)
	if err != nil {
		return ctx, false
	}
	user.CreatedAt = createdAt
	user.LastLoginAt = lastLogin
	if user.Status != "active" {
		return ctx, false
	}
	if time.Since(lastSeen) > 5*time.Minute {
		_, _ = s.db.Pool.Exec(ctx, `update sessions set last_seen_at = now() where id = $1::uuid`, sessionID)
	}
	ctx = context.WithValue(ctx, ctxUser, user)
	ctx = context.WithValue(ctx, sessionInfoKey, sessionInfo{ID: sessionID, ExpiresAt: expiresAt})
	ctx = httpx.SetActor(ctx, user.ID)
	return ctx, true
}

const sessionInfoKey ctxKey = 1

// Middleware attaches the current identity to the request context.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := s.Authenticate(r.Context(), r)
		if expected := r.Header.Get("X-RKA-User-ID"); expected != "" {
			if user, ok := UserFrom(ctx); ok && user.ID != expected {
				httpx.Error(w, r, httpx.NewError(http.StatusConflict, "account_changed", "登录账号已变化，请重新打开页面。待同步数据仍保存在原账号下。"))
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserFrom returns the authenticated user from the request context.
func UserFrom(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(ctxUser).(User)
	return user, ok
}

// MustUser returns the authenticated user or the zero value.
func MustUser(ctx context.Context) User {
	user, _ := UserFrom(ctx)
	return user
}

// SessionID returns the current login session id.
func SessionID(ctx context.Context) string {
	info, _ := ctx.Value(sessionInfoKey).(sessionInfo)
	return info.ID
}

// Require rejects anonymous requests.
func (s *Service) Require(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := UserFrom(r.Context()); !ok {
			httpx.Error(w, r, httpx.ErrUnauthorized)
			return
		}
		next(w, r)
	}
}

// RequireAdmin rejects non-administrators. The role is re-read from the
// database on every request, so a revoked role takes effect immediately.
func (s *Service) RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.Require(func(w http.ResponseWriter, r *http.Request) {
		user := MustUser(r.Context())
		if !user.IsAdmin() {
			httpx.Error(w, r, httpx.ErrForbidden)
			return
		}
		next(w, r)
	})
}

// FindUser loads one account by id.
func (s *Service) FindUser(ctx context.Context, id string) (User, error) {
	var user User
	var createdAt time.Time
	var lastLogin *time.Time
	err := s.db.Pool.QueryRow(ctx, `
		select id::text, email, display_name, role, status, must_change_password, created_at, last_login_at
		from users where id = $1::uuid`, id).
		Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.Status, &user.MustChangePassword, &createdAt, &lastLogin)
	if err != nil {
		return User{}, store.MapError(err)
	}
	user.CreatedAt = createdAt
	user.LastLoginAt = lastLogin
	return user, nil
}

// FindUserByEmail loads one account by email (case-insensitive).
func (s *Service) FindUserByEmail(ctx context.Context, email string) (User, string, error) {
	var user User
	var hash string
	var createdAt time.Time
	var lastLogin *time.Time
	err := s.db.Pool.QueryRow(ctx, `
		select id::text, email, display_name, role, status, must_change_password, password_hash, created_at, last_login_at
		from users where lower(email) = lower($1)`, normalizeEmail(email)).
		Scan(&user.ID, &user.Email, &user.DisplayName, &user.Role, &user.Status, &user.MustChangePassword, &hash, &createdAt, &lastLogin)
	if err != nil {
		return User{}, "", store.MapError(err)
	}
	user.CreatedAt = createdAt
	user.LastLoginAt = lastLogin
	return user, hash, nil
}

// CanAccessMaterial implements the shared-library rule: published materials are
// shared with every active account; anything else needs an explicit grant.
func (s *Service) CanAccessMaterial(ctx context.Context, user User, materialID string) bool {
	if user.IsAdmin() {
		return true
	}
	var status string
	if err := s.db.Pool.QueryRow(ctx, `select review_status from materials where id = $1`, materialID).Scan(&status); err != nil {
		return false
	}
	if status == "published" {
		return true
	}
	var granted bool
	_ = s.db.Pool.QueryRow(ctx, `
		select exists (
			select 1 from content_grants
			where user_id = $1::uuid and revoked_at is null
			  and resource_type = 'material' and resource_id = $2
		)`, user.ID, materialID).Scan(&granted)
	return granted
}

// HasCapability reports whether the user holds a named capability grant.
func (s *Service) HasCapability(ctx context.Context, user User, capability string) bool {
	if user.IsAdmin() {
		return true
	}
	var granted bool
	_ = s.db.Pool.QueryRow(ctx, `
		select exists (
			select 1 from content_grants
			where user_id = $1::uuid and revoked_at is null
			  and resource_type = 'capability' and resource_id = $2
		)`, user.ID, capability).Scan(&granted)
	return granted
}

// pgxTx is the transaction type accepted by repository helpers.
type pgxTx = pgx.Tx

var errInviteInvalid = httpx.NewError(http.StatusBadRequest, "invalid_invite", "邀请链接无效、已使用或已过期。")
var errBootstrapClosed = httpx.NewError(http.StatusConflict, "bootstrap_closed", "平台已经完成初始化，不能再建立首个管理员。")
var errInvalidCredentials = httpx.NewError(http.StatusUnauthorized, "invalid_credentials", "邮箱或密码不正确。")
var errDisabled = httpx.NewError(http.StatusForbidden, "account_disabled", "账号已停用，请联系管理员。")

package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"rka/internal/httpx"
	"rka/internal/store"
	"rka/internal/token"
)

// EnsureAdmin creates an administrator when the platform has none yet. It is
// used by the one-shot initialisation command and by tests. It is idempotent:
// an existing admin (or an existing account with the same email) is left alone.
func (s *Service) EnsureAdmin(ctx context.Context, email, password, displayName string) (bool, error) {
	email = normalizeEmail(email)
	if err := validateEmail(email); err != nil {
		return false, err
	}
	if err := ValidatePassword(password); err != nil {
		return false, err
	}
	if displayName == "" {
		displayName = strings.SplitN(email, "@", 2)[0]
	}
	hash, err := HashPassword(password)
	if err != nil {
		return false, err
	}
	created := false
	err = s.db.Tx(ctx, func(tx pgxTx) error {
		var admins int
		if err := tx.QueryRow(ctx, `select count(*) from users where role = 'admin'`).Scan(&admins); err != nil {
			return err
		}
		if admins > 0 {
			return nil
		}
		var existing int
		if err := tx.QueryRow(ctx, `select count(*) from users where lower(email) = $1`, email).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `
			insert into users (email, display_name, role, password_hash)
			values ($1, $2, 'admin', $3)`, email, displayName, hash); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			update platform_state set admin_bootstrapped_at = coalesce(admin_bootstrapped_at, now()),
				bootstrap_closed = true, updated_at = now() where id = 1`); err != nil {
			return err
		}
		created = true
		return nil
	})
	return created, err
}

// CreateUser creates a learner or admin account directly. Used by tests and by
// the admin API; callers are responsible for authorisation.
func (s *Service) CreateUser(ctx context.Context, email, password, displayName, role string) (User, error) {
	email = normalizeEmail(email)
	if err := validateEmail(email); err != nil {
		return User{}, err
	}
	if err := ValidatePassword(password); err != nil {
		return User{}, err
	}
	if role != "admin" && role != "learner" {
		return User{}, httpx.NewError(http.StatusBadRequest, "bad_role", "角色只能是 admin 或 learner。")
	}
	if displayName == "" {
		displayName = strings.SplitN(email, "@", 2)[0]
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	var id string
	err = s.db.Pool.QueryRow(ctx, `
		insert into users (email, display_name, role, password_hash)
		values ($1, $2, $3, $4) returning id::text`, email, displayName, role, hash).Scan(&id)
	if err != nil {
		if store.IsUniqueViolation(err) {
			return User{}, httpx.NewError(http.StatusConflict, "email_taken", "该邮箱已被使用。")
		}
		return User{}, err
	}
	return s.FindUser(ctx, id)
}

// LoginSession is returned by the test/CLI login helper.
type LoginSession struct {
	User      User
	RawToken  string
	ExpiresAt string
}

// AuthenticatePassword validates credentials without writing a browser cookie.
// It is used by CLI commands and integration tests; the HTTP handler keeps the
// cookie logic.
func (s *Service) AuthenticatePassword(ctx context.Context, email, password string) (User, error) {
	user, hash, err := s.FindUserByEmail(ctx, email)
	if err != nil {
		_ = VerifyPassword(s.dummy, password)
		return User{}, errInvalidCredentials
	}
	if !VerifyPassword(hash, password) {
		return User{}, errInvalidCredentials
	}
	if user.Status != "active" {
		return User{}, errDisabled
	}
	return user, nil
}

// CreateSessionToken issues a session and returns the raw cookie value. Tests
// use it to act as a signed-in browser.
func (s *Service) CreateSessionToken(ctx context.Context, userID, userAgent, ip string) (string, error) {
	raw, hash, err := token.New(32)
	if err != nil {
		return "", err
	}
	_, err = s.db.Pool.Exec(ctx, `
		insert into sessions (user_id, token_hash, user_agent, ip, expires_at)
		values ($1::uuid, $2, $3, $4, now() + make_interval(secs => $5))`,
		userID, hash, truncate(userAgent, 300), ip, int(s.cfg.SessionTTL.Seconds()))
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return raw, nil
}

// CookieName exposes the session cookie name to tests and to the CLI.
func (s *Service) CookieName() string { return s.cfg.CookieName }

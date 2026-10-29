package content

import (
	"encoding/json"
	"net/http"

	"rka/internal/auth"
	"rka/internal/httpx"
)

// authUser returns the authenticated user; handlers are always wrapped by
// auth.Require so this is safe.
func authUser(r *http.Request) auth.User {
	return auth.MustUser(r.Context())
}

// record writes a content administration audit row. Recording is best effort:
// an audit failure is logged but never fails the action that already happened.
func (s *Service) record(r *http.Request, action, targetType, targetID string, detail map[string]any) {
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		raw = []byte("{}")
	}
	user := auth.MustUser(r.Context())
	var actor any
	if user.ID != "" {
		actor = user.ID
	}
	_, err = s.db.Pool.Exec(r.Context(), `
		insert into audit_log (actor_user_id, action, target_type, target_id, detail, request_id, ip)
		values ($1::uuid, $2, $3, $4, $5::jsonb, $6, $7)`,
		actor, action, targetType, targetID, string(raw),
		httpx.RequestIDFrom(r.Context()), httpx.ClientIP(r, s.cfg.TrustProxy))
	if err != nil {
		s.log.Error("content audit write failed", "action", action, "target", targetID, "error", err)
	}
}

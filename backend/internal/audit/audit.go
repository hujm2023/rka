// Package audit records security-relevant actions. Recording is best effort:
// an audit failure never fails the user's request, but it is logged loudly.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"rka/internal/store"
)

type Entry struct {
	ActorID    string
	Action     string
	TargetType string
	TargetID   string
	Detail     map[string]any
	RequestID  string
	IP         string
}

type Recorder struct {
	db  *store.DB
	log *slog.Logger
}

func New(db *store.DB, log *slog.Logger) *Recorder { return &Recorder{db: db, log: log} }

// Record writes one audit row. It never returns an error.
func (r *Recorder) Record(ctx context.Context, entry Entry) {
	if entry.Detail == nil {
		entry.Detail = map[string]any{}
	}
	detail, err := json.Marshal(entry.Detail)
	if err != nil {
		detail = []byte("{}")
	}
	var actor any
	if entry.ActorID != "" {
		actor = entry.ActorID
	}
	_, err = r.db.Pool.Exec(ctx, `
		insert into audit_log (actor_user_id, action, target_type, target_id, detail, request_id, ip)
		values ($1::uuid, $2, $3, $4, $5::jsonb, $6, $7)`,
		actor, entry.Action, entry.TargetType, entry.TargetID, string(detail), entry.RequestID, entry.IP)
	if err != nil {
		r.log.Error("audit write failed", "action", entry.Action, "error", err)
	}
}

package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"rka/internal/httpx"
)

type auditItem struct {
	ID          int64     `json:"id"`
	ActorUserID string    `json:"actorUserId,omitempty"`
	ActorEmail  string    `json:"actorEmail,omitempty"`
	Action      string    `json:"action"`
	TargetType  string    `json:"targetType,omitempty"`
	TargetID    string    `json:"targetId,omitempty"`
	Detail      any       `json:"detail"`
	RequestID   string    `json:"requestId,omitempty"`
	IP          string    `json:"ip,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

// handleAuditLog exposes the append-only audit trail with cursor pagination by
// the monotonically increasing log id.
func (s *Service) handleAuditLog(w http.ResponseWriter, r *http.Request) {
	limit, _ := paginationParams(r, 50, 200)
	action := r.URL.Query().Get("action")
	actor := r.URL.Query().Get("actor")
	var beforeID int64
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			beforeID = parsed
		}
	}
	rows, err := s.db.Pool.Query(r.Context(), `
		select a.id, coalesce(a.actor_user_id::text, ''), coalesce(u.email, ''), a.action,
		       a.target_type, a.target_id, a.detail, a.request_id, a.ip, a.created_at
		from audit_log a left join users u on u.id = a.actor_user_id
		where ($1 = '' or a.action = $1)
		  and ($2 = '' or a.actor_user_id::text = $2)
		  and ($3 = 0 or a.id < $3)
		order by a.id desc limit $4`, action, actor, beforeID, limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]auditItem, 0, limit)
	for rows.Next() {
		var item auditItem
		var detailRaw []byte
		if err := rows.Scan(&item.ID, &item.ActorUserID, &item.ActorEmail, &item.Action,
			&item.TargetType, &item.TargetID, &detailRaw, &item.RequestID, &item.IP, &item.CreatedAt); err != nil {
			httpx.Error(w, r, err)
			return
		}
		if len(detailRaw) > 0 {
			_ = json.Unmarshal(detailRaw, &item.Detail)
		}
		if item.Detail == nil {
			item.Detail = map[string]any{}
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}
	nextCursor := ""
	if len(items) == limit {
		nextCursor = strconv.FormatInt(items[len(items)-1].ID, 10)
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"items": items, "nextCursor": nextCursor})
}

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"

	"rka/internal/auth"
	"rka/internal/httpx"
)

// handleExportMe returns the complete personal learning data set as JSON. Only
// the authenticated user's rows are included. Credentials and password hashes
// are never exported.
func (s *Service) handleExportMe(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	payload, err := s.collectUserExport(r.Context(), user)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="rka-export-%s.json"`, time.Now().UTC().Format("20060102T150405Z")))
	httpx.JSON(w, r, http.StatusOK, payload)
}

func (s *Service) collectUserExport(ctx context.Context, user auth.User) (map[string]any, error) {
	queries := map[string]struct {
		sql  string
		args []any
	}{
		"attempts": {
			sql: `select coalesce(jsonb_agg(to_jsonb(t) order by t.answered_at desc), '[]'::jsonb) from (
				select id, question_id, question_revision, selected, correct, assisted, duration_ms,
				       answered_at, session_id::text as session_id, created_at
				from released_attempts where user_id = $1::uuid) t`,
		},
		"marks": {
			sql: `select coalesce(jsonb_agg(to_jsonb(t) order by t.updated_at desc), '[]'::jsonb) from (
				select question_id, favorite, uncertain, note, version, updated_at
				from marks where user_id = $1::uuid) t`,
		},
		"sessions": {
			sql: `select coalesce(jsonb_agg(to_jsonb(t) order by t.created_at desc), '[]'::jsonb) from (
				select id::text, mode, scope, question_ids, revisions, index, elapsed_seconds, status,
				       bank_version, version, deadline_at, submitted_at, score, max_score, created_at, updated_at
				from practice_sessions where user_id = $1::uuid) t`,
		},
		"reading": {
			sql: `select coalesce(jsonb_agg(to_jsonb(t) order by t.updated_at desc), '[]'::jsonb) from (
				select material_id, material_version_id::text as material_version_id, progress::float8 as progress,
				       section_anchor, section_offset, done, updated_at
				from reading_progress where user_id = $1::uuid) t`,
		},
		"annotations": {
			sql: `select coalesce(jsonb_agg(to_jsonb(t) order by t.created_at desc), '[]'::jsonb) from (
				select id::text, material_id, material_version_id::text as material_version_id, section_anchor,
				       quote, note, created_at, updated_at, deleted_at
				from annotations where user_id = $1::uuid) t`,
		},
		"writingDocuments": {
			sql: `select coalesce(jsonb_agg(to_jsonb(t) order by t.updated_at desc), '[]'::jsonb) from (
				select id::text, kind, target_id, values, version, created_at, updated_at
				from writing_documents where user_id = $1::uuid) t`,
		},
		"writingVersions": {
			sql: `select coalesce(jsonb_agg(to_jsonb(t) order by t.created_at desc), '[]'::jsonb) from (
				select v.id::text, v.document_id::text as document_id, v.version, v.values, v.source, v.created_at
				from writing_versions v join writing_documents d on d.id = v.document_id
				where d.user_id = $1::uuid) t`,
		},
		"writingReviews": {
			sql: `select coalesce(jsonb_agg(to_jsonb(t) order by t.created_at desc), '[]'::jsonb) from (
				select r.id::text, r.document_id::text as document_id, r.document_version, r.status,
				       r.feedback, r.created_at, r.completed_at
				from writing_reviews r join writing_documents d on d.id = r.document_id
				where d.user_id = $1::uuid) t`,
		},
		"evidence": {
			sql: `select coalesce(jsonb_agg(to_jsonb(t) order by t.occurred_at desc), '[]'::jsonb) from (
				select id::text, kind, question_id, knowledge_point_id, material_id, rule_version, payload, occurred_at, created_at
				from evidence where user_id = $1::uuid) t`,
		},
		"mastery": {
			sql: `select coalesce(jsonb_agg(to_jsonb(t) order by t.updated_at desc), '[]'::jsonb) from (
				select knowledge_point_id, state, evidence_count, rule_version, updated_at
				from mastery where user_id = $1::uuid) t`,
		},
		"reviewItems": {
			sql: `select coalesce(jsonb_agg(to_jsonb(t) order by t.due_at), '[]'::jsonb) from (
				select question_id, due_at, interval_days, reason, updated_at
				from review_items ri where user_id = $1::uuid
                and not exists(select 1 from attempts a join practice_sessions ps on ps.id=a.session_id
                    where a.id=ri.reason->>'attemptId' and ps.mode='exam' and ps.submitted_at is null)) t`,
		},
		"aiTasks": {
			sql: `select coalesce(jsonb_agg(to_jsonb(t) order by t.created_at desc), '[]'::jsonb) from (
				select id::text, kind, status, context, attempt, error_code, error_message,
				       created_at, started_at, finished_at
				from ai_tasks where user_id = $1::uuid) t`,
		},
		"aiEvents": {
			sql: `select coalesce(jsonb_agg(to_jsonb(t) order by t.seq), '[]'::jsonb) from (
				select e.task_id::text as task_id, e.seq, e.type, e.payload, e.created_at
				from ai_events e join ai_tasks t on t.id = e.task_id
				where t.user_id = $1::uuid) t`,
		},
		"aiMessages": {
			sql: `select coalesce(jsonb_agg(to_jsonb(t) order by t.created_at), '[]'::jsonb) from (
				select m.id::text, m.task_id::text as task_id, m.role, m.content, m.created_at
				from ai_messages m join ai_tasks t on t.id = m.task_id
				where t.user_id = $1::uuid) t`,
		},
		"importedRecords": {
			sql: `select coalesce(jsonb_agg(to_jsonb(t) order by t.created_at desc), '[]'::jsonb) from (
				select id::text, source, kind, external_id, payload, note, created_at
				from imported_records where user_id = $1::uuid) t`,
		},
		"teachingIdentity": {
			sql: `select coalesce(jsonb_agg(to_jsonb(t)), '[]'::jsonb) from (
				select teaching_user_id, created_at, rotated_at
				from teaching_identities where user_id = $1::uuid) t`,
		},
	}
	data := map[string]any{}
	counts := map[string]int{}
	for name, query := range queries {
		rows, err := queryJSONRows(ctx, s.db.Pool, query.sql, user.ID)
		if err != nil {
			return nil, fmt.Errorf("导出 %s 失败：%w", name, err)
		}
		data[name] = rows
		counts[name] = len(rows)
	}
	payload := map[string]any{
		"app":           "rka",
		"schemaVersion": 2,
		"exportedAt":    time.Now().UTC(),
		"user": map[string]any{
			"id": user.ID, "email": user.Email, "displayName": user.DisplayName,
			"role": user.Role, "createdAt": user.CreatedAt,
		},
		"counts": counts,
		"data":   data,
	}
	return payload, nil
}

func queryJSONRows(ctx context.Context, q querier, sql string, args ...any) ([]any, error) {
	var raw []byte
	if err := q.QueryRow(ctx, sql, args...).Scan(&raw); err != nil {
		return nil, err
	}
	var rows []any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, err
		}
	}
	if rows == nil {
		rows = []any{}
	}
	return rows, nil
}

type deleteMyDataRequest struct {
	Confirm string `json:"confirm"`
}

// handleDeleteMyData deletes personal learning data while keeping the account
// and login sessions. The explicit confirmation token prevents accidental
// deletion from a stale browser tab.
func (s *Service) handleDeleteMyData(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	var request deleteMyDataRequest
	if err := httpx.Decode(w, r, &request, 8<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if request.Confirm != "DELETE" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "confirmation_required", `请输入 {"confirm":"DELETE"} 以确认删除全部学习数据。`))
		return
	}

	deleted := map[string]int{
		"aiEvents": 0, "aiMessages": 0, "aiTasks": 0,
		"writingReviews": 0, "writingVersions": 0, "writingDocuments": 0,
		"attempts": 0, "marks": 0, "practiceSessions": 0, "reading": 0,
		"annotations": 0, "evidence": 0, "mastery": 0, "reviewItems": 0,
		"teachingIdentity": 0, "importedRecords": 0, "exportFiles": 0,
	}
	err := s.db.Tx(r.Context(), func(tx pgx.Tx) error {
		steps := []struct {
			key string
			sql string
		}{
			{"aiEvents", `delete from ai_events where task_id in (select id from ai_tasks where user_id = $1::uuid)`},
			{"aiMessages", `delete from ai_messages where task_id in (select id from ai_tasks where user_id = $1::uuid)`},
			{"aiTasks", `delete from ai_tasks where user_id = $1::uuid`},
			{"writingReviews", `delete from writing_reviews where document_id in (select id from writing_documents where user_id = $1::uuid)`},
			{"writingVersions", `delete from writing_versions where document_id in (select id from writing_documents where user_id = $1::uuid)`},
			{"writingDocuments", `delete from writing_documents where user_id = $1::uuid`},
			{"attempts", `delete from attempts where user_id = $1::uuid`},
			{"marks", `delete from marks where user_id = $1::uuid`},
			{"practiceSessions", `delete from practice_sessions where user_id = $1::uuid`},
			{"reading", `delete from reading_progress where user_id = $1::uuid`},
			{"annotations", `delete from annotations where user_id = $1::uuid`},
			{"evidence", `delete from evidence where user_id = $1::uuid`},
			{"mastery", `delete from mastery where user_id = $1::uuid`},
			{"reviewItems", `delete from review_items where user_id = $1::uuid`},
			{"teachingIdentity", `delete from teaching_identities where user_id = $1::uuid`},
			{"importedRecords", `delete from imported_records where user_id = $1::uuid`},
		}
		for _, step := range steps {
			tag, err := tx.Exec(r.Context(), step.sql, user.ID)
			if err != nil {
				return fmt.Errorf("删除 %s 失败：%w", step.key, err)
			}
			deleted[step.key] = int(tag.RowsAffected())
		}
		return nil
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	// Remove any per-user export artifacts that may exist on disk.
	exportDir := filepath.Join(s.cfg.ExportDir(), user.ID)
	if entries, err := os.ReadDir(exportDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				if err := os.Remove(filepath.Join(exportDir, entry.Name())); err == nil {
					deleted["exportFiles"]++
				}
			}
		}
	}
	s.record(r, "me.data.delete", "user", user.ID, map[string]any{"deleted": deleted})
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"deleted": deleted,
		"kept":    []string{"账号", "登录会话", "授权"},
		"note":    "学习数据已删除；账号与登录会话保留。此操作不可撤销。",
	})
}

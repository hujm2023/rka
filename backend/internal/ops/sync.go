package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"rka/internal/auth"
	"rka/internal/httpx"
)

// handleSyncState reports server time, bank version and a per-user data digest.
// The browser uses it to decide whether its cache is still representative; the
// digest is derived from counts and timestamps, not private content.
func (s *Service) handleSyncState(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	ctx := r.Context()
	now, err := s.db.Now(ctx)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var bankVersion, contentVersion string
	_ = s.db.Pool.QueryRow(ctx, `select bank_version, content_version from platform_state where id = 1`).
		Scan(&bankVersion, &contentVersion)

	var attempts, marks, sessions, reading, annotations, writingDocuments, aiTasks int
	var lastActivity *time.Time
	if err := s.db.Pool.QueryRow(ctx, `
		select
			(select count(*) from attempts where user_id = $1::uuid),
			(select count(*) from marks where user_id = $1::uuid),
			(select count(*) from practice_sessions where user_id = $1::uuid),
			(select count(*) from reading_progress where user_id = $1::uuid),
			(select count(*) from annotations where user_id = $1::uuid and deleted_at is null),
			(select count(*) from writing_documents where user_id = $1::uuid),
			(select count(*) from ai_tasks where user_id = $1::uuid),
			coalesce(greatest(
				(select max(created_at) from attempts where user_id = $1::uuid),
				(select max(updated_at) from marks where user_id = $1::uuid),
				(select max(updated_at) from practice_sessions where user_id = $1::uuid),
				(select max(updated_at) from reading_progress where user_id = $1::uuid),
				(select max(updated_at) from writing_documents where user_id = $1::uuid),
				(select max(created_at) from ai_tasks where user_id = $1::uuid),
				(select max(created_at) from annotations where user_id = $1::uuid)
			), null)`,
		user.ID).Scan(&attempts, &marks, &sessions, &reading, &annotations, &writingDocuments, &aiTasks, &lastActivity); err != nil {
		httpx.Error(w, r, err)
		return
	}

	activeSession := any(nil)
	var sessionID string
	var sessionUpdated time.Time
	if err := s.db.Pool.QueryRow(ctx, `
		select id::text, updated_at from practice_sessions
		where user_id = $1::uuid and status = 'active' limit 1`, user.ID).Scan(&sessionID, &sessionUpdated); err == nil {
		activeSession = map[string]any{"id": sessionID, "updatedAt": sessionUpdated.UTC()}
	}

	digestSource, _ := json.Marshal(map[string]any{
		"attempts": attempts, "marks": marks, "sessions": sessions, "reading": reading,
		"annotations": annotations, "writingDocuments": writingDocuments, "aiTasks": aiTasks,
		"lastActivityAt": lastActivity,
	})
	digest := sha256Hex(digestSource)[:16]

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"serverTime":     now.UTC(),
		"bankVersion":    bankVersion,
		"contentVersion": contentVersion,
		"user": map[string]any{
			"counts": map[string]int{
				"attempts": attempts, "marks": marks, "sessions": sessions,
				"reading": reading, "annotations": annotations,
				"writingDocuments": writingDocuments, "aiTasks": aiTasks,
			},
			"lastActivityAt": lastActivity,
			"lastUpdatedAt":  lastActivity,
			"digest":         digest,
			"dataVersion":    digest,
			"activeSession":  activeSession,
		},
	})
}

// Legacy RKA v1 backup shapes (browser IndexedDB export). Unknown fields are
// ignored so backups written by older frontends remain importable, but every
// field the importer relies on is type-checked row by row.
type legacyAttempt struct {
	ID         string   `json:"id"`
	QuestionID string   `json:"questionId"`
	Revision   string   `json:"revision"`
	Answer     string   `json:"answer"`
	Correct    *bool    `json:"correct"`
	Assisted   *bool    `json:"assisted"`
	CreatedAt  string   `json:"createdAt"`
	DurationMS *float64 `json:"durationMs"`
}

type legacyMark struct {
	ID        string  `json:"id"`
	Favorite  *bool   `json:"favorite"`
	Uncertain *bool   `json:"uncertain"`
	Note      *string `json:"note"`
}

type legacyAnswerState struct {
	Selected  *string `json:"selected"`
	Submitted *bool   `json:"submitted"`
	Assisted  *bool   `json:"assisted"`
}

type legacyFilters struct {
	Mode           string   `json:"mode"`
	Module         string   `json:"module"`
	Term           string   `json:"term"`
	Source         string   `json:"source"`
	Query          string   `json:"query"`
	Count          *float64 `json:"count"`
	IncludeMissing *bool    `json:"includeMissing"`
	MaterialID     *string  `json:"materialId"`
	SectionID      *string  `json:"sectionId"`
}

type legacySession struct {
	ID          string                       `json:"id"`
	QuestionIDs []string                     `json:"questionIds"`
	Index       *int                         `json:"index"`
	Answers     map[string]legacyAnswerState `json:"answers"`
	Filters     *legacyFilters               `json:"filters"`
	Elapsed     *float64                     `json:"elapsed"`
	UpdatedAt   string                       `json:"updatedAt"`
	BankVersion string                       `json:"bankVersion"`
}

type legacyDraft struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	Values    map[string]string `json:"values"`
	UpdatedAt string            `json:"updatedAt"`
}

type legacyReading struct {
	ID            string   `json:"id"`
	Progress      *float64 `json:"progress"`
	Done          *bool    `json:"done"`
	UpdatedAt     string   `json:"updatedAt"`
	SectionID     *string  `json:"sectionId"`
	SectionOffset *float64 `json:"sectionOffset"`
}

type legacyCitation struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Text  string `json:"text"`
}

type legacyChatMessage struct {
	ID        string           `json:"id"`
	Role      string           `json:"role"`
	Content   string           `json:"content"`
	CreatedAt string           `json:"createdAt"`
	Sources   []legacyCitation `json:"sources"`
}

type legacyChat struct {
	ID        string              `json:"id"`
	Messages  []legacyChatMessage `json:"messages"`
	UpdatedAt string              `json:"updatedAt"`
}

type importIssue struct {
	Category string `json:"category"`
	ID       string `json:"id,omitempty"`
	Reason   string `json:"reason"`
}

type importResult struct {
	Imported  map[string]int `json:"imported"`
	Skipped   []importIssue  `json:"skipped"`
	Conflicts []importIssue  `json:"conflicts"`
	Counts    map[string]any `json:"counts"`
	Summary   string         `json:"summary"`
}

func (s *Service) handleSyncImport(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, 50<<20)
	decoder := json.NewDecoder(r.Body)
	var envelope syncImportEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "invalid_backup", "备份文件不是有效的 JSON："+err.Error()))
		return
	}
	if envelope.App != "rka" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "invalid_backup", "不是支持的 RKA 备份文件。"))
		return
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "invalid_backup", "备份缺少 data 字段，未修改现有记录。"))
		return
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "invalid_backup", "备份中的 data 不是 JSON 对象，未修改现有记录。"))
		return
	}
	var result importResult
	var err error
	switch envelope.SchemaVersion {
	case 1:
		// Browser RKA v1 backups keep their original semantics.
		validated, validationErr := validateLegacyBackupData(data)
		if validationErr != nil {
			httpx.Error(w, r, validationErr)
			return
		}
		result, err = s.importLegacyBackup(r.Context(), user.ID, validated)
	case 2:
		if envelope.User != nil && strings.TrimSpace(envelope.User.ID) != "" && envelope.User.ID != user.ID {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "foreign_backup", "该备份属于其他账号，只能导入到导出它的账号。"))
			return
		}
		validated, validationErr := validateV2Backup(data)
		if validationErr != nil {
			httpx.Error(w, r, validationErr)
			return
		}
		result, err = s.importV2Backup(r.Context(), user.ID, validated)
	default:
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "unsupported_schema",
			fmt.Sprintf("不支持的备份 schemaVersion: %d（仅支持 1 和 2）。", envelope.SchemaVersion)))
		return
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.record(r, "sync.import", "user", user.ID, map[string]any{
		"schemaVersion": envelope.SchemaVersion,
		"imported":      result.Imported,
		"skipped":       len(result.Skipped),
		"conflicts":     len(result.Conflicts),
	})
	httpx.JSON(w, r, http.StatusOK, result)
}

type syncImportEnvelope struct {
	App           string          `json:"app"`
	SchemaVersion int             `json:"schemaVersion"`
	Data          json.RawMessage `json:"data"`
	User          *struct {
		ID string `json:"id"`
	} `json:"user"`
}

type validatedBackup struct {
	Attempts []legacyAttempt
	Marks    []legacyMark
	Sessions []legacySession
	Drafts   []legacyDraft
	Reading  []legacyReading
	Chats    []legacyChat
	Raw      map[string][]json.RawMessage
}

func invalidBackup(category, detail string) *httpx.APIError {
	message := "备份中的 " + category + " 数据不完整，未修改现有记录。"
	if detail != "" {
		message += "（" + detail + "）"
	}
	return httpx.NewError(http.StatusBadRequest, "invalid_backup", message)
}

func validateLegacyBackupData(data map[string]json.RawMessage) (validatedBackup, error) {
	var out validatedBackup
	categories := []string{"attempts", "marks", "sessions", "drafts", "reading", "chats"}
	out.Raw = map[string][]json.RawMessage{}
	for _, category := range categories {
		raw, ok := data[category]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			return out, invalidBackup(category, "")
		}
		var rows []json.RawMessage
		if err := json.Unmarshal(raw, &rows); err != nil {
			return out, invalidBackup(category, "字段不是数组")
		}
		if len(rows) > 100000 {
			return out, httpx.NewError(http.StatusBadRequest, "backup_too_large", "备份中的 "+category+" 记录超过 100000 条，未导入。")
		}
		seen := map[string]bool{}
		for i, row := range rows {
			var id struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(row, &id); err != nil || strings.TrimSpace(id.ID) == "" {
				return out, invalidBackup(category, fmt.Sprintf("第 %d 条缺少 id", i+1))
			}
			if seen[id.ID] {
				return out, invalidBackup(category, "存在重复 ID "+id.ID)
			}
			seen[id.ID] = true
		}
		out.Raw[category] = rows
	}

	// attempts
	for _, raw := range out.Raw["attempts"] {
		var row legacyAttempt
		if err := json.Unmarshal(raw, &row); err != nil {
			return out, invalidBackup("attempts", "")
		}
		if strings.TrimSpace(row.QuestionID) == "" || strings.TrimSpace(row.Revision) == "" {
			return out, invalidBackup("attempts", "")
		}
		if !validOption(row.Answer) || row.Correct == nil || row.Assisted == nil ||
			row.DurationMS == nil || *row.DurationMS < 0 || !parseLegacyDate(row.CreatedAt) {
			return out, invalidBackup("attempts", "")
		}
		out.Attempts = append(out.Attempts, row)
	}
	// marks
	for _, raw := range out.Raw["marks"] {
		var row legacyMark
		if err := json.Unmarshal(raw, &row); err != nil || row.Favorite == nil || row.Uncertain == nil || row.Note == nil {
			return out, invalidBackup("marks", "")
		}
		out.Marks = append(out.Marks, row)
	}
	// sessions
	for _, raw := range out.Raw["sessions"] {
		var row legacySession
		if err := json.Unmarshal(raw, &row); err != nil || row.Index == nil || row.Elapsed == nil || *row.Elapsed < 0 {
			return out, invalidBackup("sessions", "")
		}
		if row.Answers == nil || row.Filters == nil || !parseLegacyDate(row.UpdatedAt) || strings.TrimSpace(row.BankVersion) == "" {
			return out, invalidBackup("sessions", "")
		}
		if *row.Index < 0 || *row.Index >= maxInt(1, len(row.QuestionIDs)) {
			return out, invalidBackup("sessions", "")
		}
		for _, id := range row.QuestionIDs {
			if strings.TrimSpace(id) == "" {
				return out, invalidBackup("sessions", "")
			}
		}
		filters := row.Filters
		if !validMode(filters.Mode) || filters.Count == nil || *filters.Count <= 0 || filters.IncludeMissing == nil {
			return out, invalidBackup("sessions", "")
		}
		for _, state := range row.Answers {
			if state.Selected != nil && !validOption(*state.Selected) {
				return out, invalidBackup("sessions", "")
			}
		}
		out.Sessions = append(out.Sessions, row)
	}
	// drafts
	for _, raw := range out.Raw["drafts"] {
		var row legacyDraft
		if err := json.Unmarshal(raw, &row); err != nil || row.Values == nil || !parseLegacyDate(row.UpdatedAt) {
			return out, invalidBackup("drafts", "")
		}
		if row.Kind != "case" && row.Kind != "essay" {
			return out, invalidBackup("drafts", "")
		}
		out.Drafts = append(out.Drafts, row)
	}
	// reading
	for _, raw := range out.Raw["reading"] {
		var row legacyReading
		if err := json.Unmarshal(raw, &row); err != nil || row.Progress == nil || row.Done == nil ||
			*row.Progress < 0 || *row.Progress > 1 || !parseLegacyDate(row.UpdatedAt) {
			return out, invalidBackup("reading", "")
		}
		out.Reading = append(out.Reading, row)
	}
	// chats
	for _, raw := range out.Raw["chats"] {
		var row legacyChat
		if err := json.Unmarshal(raw, &row); err != nil || row.Messages == nil || !parseLegacyDate(row.UpdatedAt) {
			return out, invalidBackup("chats", "")
		}
		for _, message := range row.Messages {
			if strings.TrimSpace(message.ID) == "" || (message.Role != "user" && message.Role != "assistant") ||
				!parseLegacyDate(message.CreatedAt) {
				return out, invalidBackup("chats", "")
			}
			for _, source := range message.Sources {
				if strings.TrimSpace(source.ID) == "" || strings.TrimSpace(source.Title) == "" {
					return out, invalidBackup("chats", "")
				}
			}
		}
		out.Chats = append(out.Chats, row)
	}
	return out, nil
}

func (s *Service) importLegacyBackup(ctx context.Context, userID string, backup validatedBackup) (importResult, error) {
	result := importResult{
		Imported:  map[string]int{"attempts": 0, "marks": 0, "sessions": 0, "drafts": 0, "reading": 0, "chats": 0},
		Skipped:   []importIssue{},
		Conflicts: []importIssue{},
	}
	const issueLimit = 100
	addSkipped := func(issue importIssue) {
		if len(result.Skipped) < issueLimit {
			result.Skipped = append(result.Skipped, issue)
		}
	}
	addConflict := func(issue importIssue) {
		if len(result.Conflicts) < issueLimit {
			result.Conflicts = append(result.Conflicts, issue)
		}
	}
	skippedTotal, conflictTotal := 0, 0

	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		// attempts ---------------------------------------------------------
		for _, row := range backup.Attempts {
			revision, answer, currentRevision, found, err := loadQuestionForImport(ctx, tx, row.QuestionID)
			if err != nil {
				return err
			}
			if !found {
				skippedTotal++
				addSkipped(importIssue{Category: "attempts", ID: row.ID, Reason: "题目不存在或已下线：" + row.QuestionID})
				continue
			}
			if row.Revision != revision {
				if historicalAnswer, ok, err := loadHistoricalAnswer(ctx, tx, row.QuestionID, row.Revision); err != nil {
					return err
				} else if ok {
					answer = historicalAnswer
					revision = row.Revision
				} else {
					conflictTotal++
					addConflict(importIssue{
						Category: "attempts", ID: row.ID,
						Reason: fmt.Sprintf("题目版本 %s 已不存在，按当前版本 %s 导入。", row.Revision, currentRevision),
					})
					revision = currentRevision
				}
			}
			correct := row.Answer == answer
			if row.Correct != nil && *row.Correct != correct {
				conflictTotal++
				addConflict(importIssue{
					Category: "attempts", ID: row.ID,
					Reason: fmt.Sprintf("备份记录的正确性为 %v，按服务器答案重算为 %v。", *row.Correct, correct),
				})
			}
			duration := int(*row.DurationMS)
			if duration < 0 {
				duration = 0
			}
			answeredAt, _ := time.Parse(time.RFC3339Nano, row.CreatedAt)
			tag, err := tx.Exec(ctx, `
				insert into attempts (id, user_id, session_id, question_id, question_revision, selected, correct, assisted, duration_ms, answered_at, client_seq)
				values ($1, $2::uuid, null, $3, $4, $5, $6, $7, $8, $9, null)
				on conflict (id) do nothing`,
				importAttemptID(userID, row.ID), userID, row.QuestionID, revision, row.Answer, correct, *row.Assisted, duration, answeredAt)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				result.Imported["attempts"]++
			} else {
				skippedTotal++
				addSkipped(importIssue{Category: "attempts", ID: row.ID, Reason: "已导入（幂等跳过）"})
			}
		}

		// marks ------------------------------------------------------------
		for _, row := range backup.Marks {
			exists, err := questionExists(ctx, tx, row.ID)
			if err != nil {
				return err
			}
			if !exists {
				skippedTotal++
				addSkipped(importIssue{Category: "marks", ID: row.ID, Reason: "题目不存在或已下线：" + row.ID})
				continue
			}
			var favorite, uncertain bool
			var note string
			var version int
			err = tx.QueryRow(ctx, `
				select favorite, uncertain, note, version from marks
				where user_id = $1::uuid and question_id = $2`, userID, row.ID).
				Scan(&favorite, &uncertain, &note, &version)
			if err == nil {
				if favorite == *row.Favorite && uncertain == *row.Uncertain && note == *row.Note {
					skippedTotal++
					addSkipped(importIssue{Category: "marks", ID: row.ID, Reason: "已存在相同标记（幂等跳过）"})
				} else {
					conflictTotal++
					addConflict(importIssue{Category: "marks", ID: row.ID, Reason: "服务器已有不同标记，保留服务器版本。"})
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if _, err := tx.Exec(ctx, `
				insert into marks (user_id, question_id, favorite, uncertain, note, version, updated_at)
				values ($1::uuid, $2, $3, $4, $5, 1, now())`,
				userID, row.ID, *row.Favorite, *row.Uncertain, *row.Note); err != nil {
				return err
			}
			result.Imported["marks"]++
		}

		// reading ----------------------------------------------------------
		for _, row := range backup.Reading {
			var materialVersionText string
			err := tx.QueryRow(ctx, `select coalesce(current_version_id::text, '') from materials where id = $1`, row.ID).Scan(&materialVersionText)
			var materialVersionID *string
			if materialVersionText != "" {
				materialVersionID = &materialVersionText
			}
			if errors.Is(err, pgx.ErrNoRows) {
				skippedTotal++
				addSkipped(importIssue{Category: "reading", ID: row.ID, Reason: "资料不存在或已下线：" + row.ID})
				continue
			}
			if err != nil {
				return err
			}
			var existingProgress float64
			var existingDone bool
			err = tx.QueryRow(ctx, `
				select progress::float8, done from reading_progress
				where user_id = $1::uuid and material_id = $2`, userID, row.ID).Scan(&existingProgress, &existingDone)
			if err == nil {
				if existingProgress >= *row.Progress && (existingDone || !*row.Done) {
					skippedTotal++
					addSkipped(importIssue{Category: "reading", ID: row.ID, Reason: "服务器进度不落后于备份（幂等跳过）"})
					continue
				}
				sectionAnchor := ""
				if row.SectionID != nil {
					sectionAnchor = *row.SectionID
				}
				offset := 0
				if row.SectionOffset != nil {
					offset = int(*row.SectionOffset)
				}
				if _, err := tx.Exec(ctx, `
					update reading_progress set
						progress = greatest(progress, $3),
						material_version_id = coalesce(material_version_id, $4::uuid),
						section_anchor = case when $3 >= progress then $5 else section_anchor end,
						section_offset = case when $3 >= progress then $6 else section_offset end,
						done = done or $7,
						updated_at = now()
					where user_id = $1::uuid and material_id = $2`,
					userID, row.ID, *row.Progress, materialVersionID, sectionAnchor, offset, *row.Done); err != nil {
					return err
				}
				result.Imported["reading"]++
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			sectionAnchor := ""
			if row.SectionID != nil {
				sectionAnchor = *row.SectionID
			}
			offset := 0
			if row.SectionOffset != nil {
				offset = int(*row.SectionOffset)
			}
			if _, err := tx.Exec(ctx, `
				insert into reading_progress (user_id, material_id, material_version_id, progress, section_anchor, section_offset, done, updated_at)
				values ($1::uuid, $2, $3::uuid, $4, $5, $6, $7, now())`,
				userID, row.ID, materialVersionID, *row.Progress, sectionAnchor, offset, *row.Done); err != nil {
				return err
			}
			result.Imported["reading"]++
		}

		// drafts -----------------------------------------------------------
		for _, row := range backup.Drafts {
			var exists bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from subjective_items where id = $1 and kind = $2)`, row.ID, row.Kind).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				skippedTotal++
				addSkipped(importIssue{Category: "drafts", ID: row.ID, Reason: "写作题目不存在：" + row.ID})
				continue
			}
			values, err := json.Marshal(row.Values)
			if err != nil {
				return err
			}
			var existing []byte
			var documentID string
			err = tx.QueryRow(ctx, `
				select id::text, values from writing_documents
				where user_id = $1::uuid and kind = $2 and target_id = $3`, userID, row.Kind, row.ID).Scan(&documentID, &existing)
			if err == nil {
				var current map[string]string
				_ = json.Unmarshal(existing, &current)
				if valuesEqual(current, row.Values) {
					skippedTotal++
					addSkipped(importIssue{Category: "drafts", ID: row.ID, Reason: "草稿已存在且内容一致（幂等跳过）"})
				} else {
					conflictTotal++
					addConflict(importIssue{Category: "drafts", ID: row.ID, Reason: "服务器已有更新的草稿，保留服务器版本。"})
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			updatedAt, _ := time.Parse(time.RFC3339Nano, row.UpdatedAt)
			if err := tx.QueryRow(ctx, `
				insert into writing_documents (user_id, kind, target_id, values, version, created_at, updated_at)
				values ($1::uuid, $2, $3, $4::jsonb, 1, $5, $5)
				returning id::text`, userID, row.Kind, row.ID, string(values), updatedAt).Scan(&documentID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				insert into writing_versions (document_id, version, values, source, created_by, created_at)
				values ($1::uuid, 1, $2::jsonb, 'user', $3::uuid, $4)
				on conflict do nothing`, documentID, string(values), userID, updatedAt); err != nil {
				return err
			}
			result.Imported["drafts"]++
		}

		// sessions and chats are preserved as read-only imported records -----
		for _, row := range backup.Sessions {
			raw := findRaw(backup.Raw["sessions"], row.ID)
			if inserts, err := insertImportedRecord(ctx, tx, userID, "session", row.ID, raw, "旧版浏览器练习现场，仅存档；服务端不恢复旧练习现场。"); err != nil {
				return err
			} else if inserts > 0 {
				result.Imported["sessions"]++
			} else {
				skippedTotal++
				addSkipped(importIssue{Category: "sessions", ID: row.ID, Reason: "已存档（幂等跳过）"})
			}
		}
		for _, row := range backup.Chats {
			raw := findRaw(backup.Raw["chats"], row.ID)
			if inserts, err := insertImportedRecord(ctx, tx, userID, "chat", row.ID, raw, "旧版浏览器 AI 对话，仅存档。"); err != nil {
				return err
			} else if inserts > 0 {
				result.Imported["chats"]++
			} else {
				skippedTotal++
				addSkipped(importIssue{Category: "chats", ID: row.ID, Reason: "已存档（幂等跳过）"})
			}
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	result.Counts = map[string]any{
		"skipped":   skippedTotal,
		"conflicts": conflictTotal,
		"truncated": skippedTotal > len(result.Skipped) || conflictTotal > len(result.Conflicts),
	}
	total := 0
	for _, count := range result.Imported {
		total += count
	}
	result.Summary = fmt.Sprintf("已导入 %d 条记录，跳过 %d 条，冲突 %d 条。", total, skippedTotal, conflictTotal)
	return result, nil
}

func findRaw(rows []json.RawMessage, id string) json.RawMessage {
	for _, raw := range rows {
		var candidate struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &candidate) == nil && candidate.ID == id {
			return raw
		}
	}
	return json.RawMessage("{}")
}

func loadQuestionForImport(ctx context.Context, tx pgx.Tx, questionID string) (revision, answer, currentRevision string, found bool, err error) {
	err = tx.QueryRow(ctx, `
		select qv.revision, qv.answer
		from questions q join question_versions qv on qv.id = q.current_version_id
		where q.id = $1`, questionID).Scan(&revision, &answer)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", false, err
	}
	return revision, answer, revision, true, nil
}

func loadHistoricalAnswer(ctx context.Context, tx pgx.Tx, questionID, revision string) (string, bool, error) {
	var answer string
	err := tx.QueryRow(ctx, `select answer from question_versions where question_id = $1 and revision = $2`, questionID, revision).Scan(&answer)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return answer, true, nil
}

func questionExists(ctx context.Context, tx pgx.Tx, questionID string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `select exists (select 1 from questions where id = $1)`, questionID).Scan(&exists)
	return exists, err
}

func insertImportedRecord(ctx context.Context, tx pgx.Tx, userID, kind, externalID string, payload json.RawMessage, note string) (int64, error) {
	tag, err := tx.Exec(ctx, `
		insert into imported_records (user_id, source, kind, external_id, payload, note)
		values ($1::uuid, 'rka-v1', $2, $3, $4::jsonb, $5)
		on conflict (user_id, source, kind, external_id) do nothing`,
		userID, kind, externalID, string(payload), note)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func importAttemptID(userID, legacyID string) string {
	sum := sha256.Sum256([]byte(userID + "\x00" + legacyID))
	return "imp-" + hex.EncodeToString(sum[:16])
}

func valuesEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func validOption(value string) bool {
	switch value {
	case "A", "B", "C", "D":
		return true
	}
	return false
}

func validMode(value string) bool {
	switch value {
	case "chapter", "all", "exam", "wrong", "favorites", "review":
		return true
	}
	return false
}

func parseLegacyDate(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

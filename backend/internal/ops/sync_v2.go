package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"rka/internal/httpx"
)

// RKA v2 backup shapes. These mirror exactly what GET /api/export/me emits
// (snake_case columns from to_jsonb), so a user can export their data, delete
// it and import the same file back. Unknown fields are ignored; every field the
// importer depends on is validated row by row.

type v2Attempt struct {
	ID               string   `json:"id"`
	QuestionID       string   `json:"question_id"`
	QuestionRevision string   `json:"question_revision"`
	Selected         string   `json:"selected"`
	Correct          *bool    `json:"correct"`
	Assisted         *bool    `json:"assisted"`
	DurationMS       *float64 `json:"duration_ms"`
	AnsweredAt       string   `json:"answered_at"`
	SessionID        *string  `json:"session_id"`
	CreatedAt        *string  `json:"created_at"`
}

type v2Mark struct {
	QuestionID string  `json:"question_id"`
	Favorite   *bool   `json:"favorite"`
	Uncertain  *bool   `json:"uncertain"`
	Note       *string `json:"note"`
	Version    *int    `json:"version"`
	UpdatedAt  *string `json:"updated_at"`
}

type v2Session struct {
	ID             string         `json:"id"`
	Mode           string         `json:"mode"`
	Scope          map[string]any `json:"scope"`
	QuestionIDs    []string       `json:"question_ids"`
	Revisions      map[string]any `json:"revisions"`
	Index          *int           `json:"index"`
	ElapsedSeconds *int           `json:"elapsed_seconds"`
	Status         string         `json:"status"`
	BankVersion    string         `json:"bank_version"`
	Version        *int           `json:"version"`
	DeadlineAt     *string        `json:"deadline_at"`
	SubmittedAt    *string        `json:"submitted_at"`
	Score          *int           `json:"score"`
	MaxScore       *int           `json:"max_score"`
	CreatedAt      *string        `json:"created_at"`
	UpdatedAt      *string        `json:"updated_at"`
}

type v2Reading struct {
	MaterialID        string   `json:"material_id"`
	MaterialVersionID *string  `json:"material_version_id"`
	Progress          *float64 `json:"progress"`
	SectionAnchor     *string  `json:"section_anchor"`
	SectionOffset     *int     `json:"section_offset"`
	Done              *bool    `json:"done"`
	UpdatedAt         *string  `json:"updated_at"`
}

type v2Annotation struct {
	ID                string  `json:"id"`
	MaterialID        string  `json:"material_id"`
	MaterialVersionID *string `json:"material_version_id"`
	SectionAnchor     *string `json:"section_anchor"`
	Quote             *string `json:"quote"`
	Note              *string `json:"note"`
	CreatedAt         *string `json:"created_at"`
	UpdatedAt         *string `json:"updated_at"`
	DeletedAt         *string `json:"deleted_at"`
}

type v2WritingDocument struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	TargetID  string         `json:"target_id"`
	Values    map[string]any `json:"values"`
	Version   *int           `json:"version"`
	CreatedAt *string        `json:"created_at"`
	UpdatedAt *string        `json:"updated_at"`
}

type v2WritingVersion struct {
	ID         string         `json:"id"`
	DocumentID string         `json:"document_id"`
	Version    *int           `json:"version"`
	Values     map[string]any `json:"values"`
	Source     string         `json:"source"`
	CreatedAt  *string        `json:"created_at"`
}

type v2WritingReview struct {
	ID              string          `json:"id"`
	DocumentID      string          `json:"document_id"`
	DocumentVersion *int            `json:"document_version"`
	Status          string          `json:"status"`
	Feedback        json.RawMessage `json:"feedback"`
	CreatedAt       *string         `json:"created_at"`
	CompletedAt     *string         `json:"completed_at"`
}

type v2Evidence struct {
	ID               string         `json:"id"`
	Kind             string         `json:"kind"`
	QuestionID       *string        `json:"question_id"`
	KnowledgePointID *string        `json:"knowledge_point_id"`
	MaterialID       *string        `json:"material_id"`
	RuleVersion      *string        `json:"rule_version"`
	Payload          map[string]any `json:"payload"`
	OccurredAt       string         `json:"occurred_at"`
	CreatedAt        *string        `json:"created_at"`
}

type v2Mastery struct {
	KnowledgePointID string  `json:"knowledge_point_id"`
	State            string  `json:"state"`
	EvidenceCount    *int    `json:"evidence_count"`
	RuleVersion      *string `json:"rule_version"`
	UpdatedAt        *string `json:"updated_at"`
}

type v2ReviewItem struct {
	QuestionID   string         `json:"question_id"`
	DueAt        string         `json:"due_at"`
	IntervalDays *int           `json:"interval_days"`
	Reason       map[string]any `json:"reason"`
	UpdatedAt    *string        `json:"updated_at"`
}

type v2AITask struct {
	ID           string         `json:"id"`
	Kind         string         `json:"kind"`
	Status       string         `json:"status"`
	Context      map[string]any `json:"context"`
	Attempt      *int           `json:"attempt"`
	ErrorCode    *string        `json:"error_code"`
	ErrorMessage *string        `json:"error_message"`
	CreatedAt    *string        `json:"created_at"`
	StartedAt    *string        `json:"started_at"`
	FinishedAt   *string        `json:"finished_at"`
}

type v2AIEvent struct {
	TaskID    string         `json:"task_id"`
	Seq       *int           `json:"seq"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
	CreatedAt *string        `json:"created_at"`
}

type v2AIMessage struct {
	ID        string  `json:"id"`
	TaskID    string  `json:"task_id"`
	Role      string  `json:"role"`
	Content   string  `json:"content"`
	CreatedAt *string `json:"created_at"`
}

type v2ImportedRecord struct {
	ID         string         `json:"id"`
	Source     string         `json:"source"`
	Kind       string         `json:"kind"`
	ExternalID string         `json:"external_id"`
	Payload    map[string]any `json:"payload"`
	Note       *string        `json:"note"`
	CreatedAt  *string        `json:"created_at"`
}

type v2TeachingIdentity struct {
	TeachingUserID string  `json:"teaching_user_id"`
	CreatedAt      *string `json:"created_at"`
	RotatedAt      *string `json:"rotated_at"`
}

type v2Backup struct {
	Attempts         []v2Attempt
	Marks            []v2Mark
	Sessions         []v2Session
	Reading          []v2Reading
	Annotations      []v2Annotation
	WritingDocuments []v2WritingDocument
	WritingVersions  []v2WritingVersion
	WritingReviews   []v2WritingReview
	Evidence         []v2Evidence
	Mastery          []v2Mastery
	ReviewItems      []v2ReviewItem
	AITasks          []v2AITask
	AIEvents         []v2AIEvent
	AIMessages       []v2AIMessage
	ImportedRecords  []v2ImportedRecord
	TeachingIdentity []v2TeachingIdentity
}

var v2Categories = []string{
	"attempts", "marks", "sessions", "reading", "annotations",
	"writingDocuments", "writingVersions", "writingReviews",
	"evidence", "mastery", "reviewItems",
	"aiTasks", "aiEvents", "aiMessages", "importedRecords", "teachingIdentity",
}

// v2Rows reads one resource array. Missing or null categories are treated as
// empty so a partial export is still importable; wrong types are rejected.
func v2Rows(data map[string]json.RawMessage, category string) ([]json.RawMessage, error) {
	raw, ok := data[category]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, httpx.NewError(400, "invalid_backup", "备份中的 "+category+" 字段不是数组，未修改现有记录。")
	}
	if len(rows) > 100000 {
		return nil, httpx.NewError(400, "backup_too_large", "备份中的 "+category+" 记录超过 100000 条，未导入。")
	}
	return rows, nil
}

func decodeV2Rows[T any](rows []json.RawMessage, category string, valid func(T) bool) ([]T, error) {
	out := make([]T, 0, len(rows))
	for i, raw := range rows {
		var row T
		if err := json.Unmarshal(raw, &row); err != nil || !valid(row) {
			return nil, invalidBackup(category, fmt.Sprintf("第 %d 条字段不正确", i+1))
		}
		out = append(out, row)
	}
	return out, nil
}

func validateV2Backup(data map[string]json.RawMessage) (v2Backup, error) {
	var out v2Backup
	rows := map[string][]json.RawMessage{}
	for _, category := range v2Categories {
		decoded, err := v2Rows(data, category)
		if err != nil {
			return out, err
		}
		rows[category] = decoded
	}
	var err error
	if out.Attempts, err = decodeV2Rows(rows["attempts"], "attempts", func(row v2Attempt) bool {
		return strings.TrimSpace(row.ID) != "" && strings.TrimSpace(row.QuestionID) != "" &&
			validOption(row.Selected) && row.Correct != nil && row.Assisted != nil &&
			row.DurationMS != nil && *row.DurationMS >= 0 && parseLegacyDate(row.AnsweredAt)
	}); err != nil {
		return out, err
	}
	if out.Marks, err = decodeV2Rows(rows["marks"], "marks", func(row v2Mark) bool {
		return strings.TrimSpace(row.QuestionID) != "" && row.Favorite != nil && row.Uncertain != nil && row.Note != nil
	}); err != nil {
		return out, err
	}
	if out.Sessions, err = decodeV2Rows(rows["sessions"], "sessions", func(row v2Session) bool {
		if strings.TrimSpace(row.ID) == "" || !uuidPattern.MatchString(row.ID) {
			return false
		}
		if !validMode(row.Mode) || !validSessionStatus(row.Status) || row.Index == nil || row.ElapsedSeconds == nil {
			return false
		}
		if *row.Index < 0 || *row.ElapsedSeconds < 0 {
			return false
		}
		if row.Version == nil || *row.Version < 1 {
			return false
		}
		for _, id := range row.QuestionIDs {
			if strings.TrimSpace(id) == "" {
				return false
			}
		}
		return optionalDateValid(row.CreatedAt) && optionalDateValid(row.UpdatedAt) &&
			optionalDateValid(row.DeadlineAt) && optionalDateValid(row.SubmittedAt)
	}); err != nil {
		return out, err
	}
	if out.Reading, err = decodeV2Rows(rows["reading"], "reading", func(row v2Reading) bool {
		return strings.TrimSpace(row.MaterialID) != "" && row.Progress != nil && *row.Progress >= 0 && *row.Progress <= 1 &&
			row.Done != nil && row.SectionOffset != nil && *row.SectionOffset >= 0 &&
			optionalDateValid(row.UpdatedAt)
	}); err != nil {
		return out, err
	}
	if out.Annotations, err = decodeV2Rows(rows["annotations"], "annotations", func(row v2Annotation) bool {
		return strings.TrimSpace(row.ID) != "" && uuidPattern.MatchString(row.ID) &&
			strings.TrimSpace(row.MaterialID) != "" && row.Quote != nil && row.Note != nil &&
			optionalDateValid(row.CreatedAt) && optionalDateValid(row.UpdatedAt) && optionalDateValid(row.DeletedAt)
	}); err != nil {
		return out, err
	}
	if out.WritingDocuments, err = decodeV2Rows(rows["writingDocuments"], "writingDocuments", func(row v2WritingDocument) bool {
		return strings.TrimSpace(row.ID) != "" && uuidPattern.MatchString(row.ID) &&
			(row.Kind == "case" || row.Kind == "essay") && strings.TrimSpace(row.TargetID) != "" &&
			row.Values != nil && row.Version != nil && *row.Version >= 1 &&
			optionalDateValid(row.CreatedAt) && optionalDateValid(row.UpdatedAt)
	}); err != nil {
		return out, err
	}
	if out.WritingVersions, err = decodeV2Rows(rows["writingVersions"], "writingVersions", func(row v2WritingVersion) bool {
		return strings.TrimSpace(row.ID) != "" && uuidPattern.MatchString(row.ID) &&
			strings.TrimSpace(row.DocumentID) != "" && uuidPattern.MatchString(row.DocumentID) &&
			row.Version != nil && *row.Version >= 1 && row.Values != nil &&
			(row.Source == "user" || row.Source == "conflict") && optionalDateValid(row.CreatedAt)
	}); err != nil {
		return out, err
	}
	if out.WritingReviews, err = decodeV2Rows(rows["writingReviews"], "writingReviews", func(row v2WritingReview) bool {
		return strings.TrimSpace(row.ID) != "" && uuidPattern.MatchString(row.ID) &&
			strings.TrimSpace(row.DocumentID) != "" && uuidPattern.MatchString(row.DocumentID) &&
			row.DocumentVersion != nil && *row.DocumentVersion >= 1 && validWritingReviewStatus(row.Status) &&
			optionalDateValid(row.CreatedAt) && optionalDateValid(row.CompletedAt)
	}); err != nil {
		return out, err
	}
	if out.Evidence, err = decodeV2Rows(rows["evidence"], "evidence", func(row v2Evidence) bool {
		return strings.TrimSpace(row.ID) != "" && uuidPattern.MatchString(row.ID) &&
			validEvidenceKind(row.Kind) && parseLegacyDate(row.OccurredAt) && optionalDateValid(row.CreatedAt)
	}); err != nil {
		return out, err
	}
	if out.Mastery, err = decodeV2Rows(rows["mastery"], "mastery", func(row v2Mastery) bool {
		return strings.TrimSpace(row.KnowledgePointID) != "" && validMasteryState(row.State) &&
			row.EvidenceCount != nil && *row.EvidenceCount >= 0 && optionalDateValid(row.UpdatedAt)
	}); err != nil {
		return out, err
	}
	if out.ReviewItems, err = decodeV2Rows(rows["reviewItems"], "reviewItems", func(row v2ReviewItem) bool {
		return strings.TrimSpace(row.QuestionID) != "" && parseLegacyDate(row.DueAt) &&
			row.IntervalDays != nil && *row.IntervalDays >= 0 && optionalDateValid(row.UpdatedAt)
	}); err != nil {
		return out, err
	}
	if out.AITasks, err = decodeV2Rows(rows["aiTasks"], "aiTasks", func(row v2AITask) bool {
		return strings.TrimSpace(row.ID) != "" && uuidPattern.MatchString(row.ID) &&
			validAITaskKind(row.Kind) && validAITaskStatus(row.Status) && row.Attempt != nil && *row.Attempt >= 0 &&
			optionalDateValid(row.CreatedAt) && optionalDateValid(row.StartedAt) && optionalDateValid(row.FinishedAt)
	}); err != nil {
		return out, err
	}
	if out.AIEvents, err = decodeV2Rows(rows["aiEvents"], "aiEvents", func(row v2AIEvent) bool {
		return strings.TrimSpace(row.TaskID) != "" && uuidPattern.MatchString(row.TaskID) &&
			row.Seq != nil && *row.Seq > 0 && strings.TrimSpace(row.Type) != "" && optionalDateValid(row.CreatedAt)
	}); err != nil {
		return out, err
	}
	if out.AIMessages, err = decodeV2Rows(rows["aiMessages"], "aiMessages", func(row v2AIMessage) bool {
		return strings.TrimSpace(row.ID) != "" && uuidPattern.MatchString(row.ID) &&
			strings.TrimSpace(row.TaskID) != "" && uuidPattern.MatchString(row.TaskID) &&
			validAIMessageRole(row.Role) && optionalDateValid(row.CreatedAt)
	}); err != nil {
		return out, err
	}
	if out.ImportedRecords, err = decodeV2Rows(rows["importedRecords"], "importedRecords", func(row v2ImportedRecord) bool {
		return strings.TrimSpace(row.ID) != "" && uuidPattern.MatchString(row.ID) &&
			strings.TrimSpace(row.Source) != "" && (row.Kind == "session" || row.Kind == "chat") &&
			strings.TrimSpace(row.ExternalID) != "" && optionalDateValid(row.CreatedAt)
	}); err != nil {
		return out, err
	}
	if out.TeachingIdentity, err = decodeV2Rows(rows["teachingIdentity"], "teachingIdentity", func(row v2TeachingIdentity) bool {
		return strings.TrimSpace(row.TeachingUserID) != "" &&
			optionalDateValid(row.CreatedAt) && optionalDateValid(row.RotatedAt)
	}); err != nil {
		return out, err
	}
	return out, nil
}

func optionalDateValid(value *string) bool {
	return value == nil || *value == "" || parseLegacyDate(*value)
}

func validSessionStatus(value string) bool {
	switch value {
	case "active", "finished", "abandoned":
		return true
	}
	return false
}

func validWritingReviewStatus(value string) bool {
	switch value {
	case "queued", "running", "completed", "failed", "cancelled":
		return true
	}
	return false
}

func validEvidenceKind(value string) bool {
	switch value {
	case "reading", "hint_used", "assisted_pass", "independent_pass", "wrong", "self_declared":
		return true
	}
	return false
}

func validMasteryState(value string) bool {
	switch value {
	case "unknown", "read", "assisted", "independent", "self_declared":
		return true
	}
	return false
}

func validAITaskKind(value string) bool {
	switch value {
	case "hint", "explain", "followup", "review_case", "review_essay", "path", "report":
		return true
	}
	return false
}

func validAITaskStatus(value string) bool {
	switch value {
	case "queued", "running", "waiting_user", "completed", "failed", "cancelling", "cancelled", "recovering":
		return true
	}
	return false
}

func validAIMessageRole(value string) bool {
	return value == "user" || value == "assistant" || value == "system"
}

func masteryRank(state string) int {
	switch state {
	case "read":
		return 1
	case "assisted":
		return 2
	case "independent":
		return 3
	case "self_declared":
		return 4
	default:
		return 0
	}
}

// importV2Backup restores a personal export into the current account. Every row
// is checked for ownership first: a matching primary key that belongs to another
// account is reported as a conflict and never modified. Natural-key resources
// merge conservatively; newer server drafts are never overwritten.
func (s *Service) importV2Backup(ctx context.Context, userID string, backup v2Backup) (importResult, error) {
	result := importResult{
		Imported: map[string]int{
			"attempts": 0, "marks": 0, "sessions": 0, "reading": 0, "annotations": 0,
			"writingDocuments": 0, "writingVersions": 0, "writingReviews": 0,
			"evidence": 0, "mastery": 0, "reviewItems": 0,
			"aiTasks": 0, "aiEvents": 0, "aiMessages": 0,
			"importedRecords": 0, "teachingIdentity": 0,
		},
		Skipped:   []importIssue{},
		Conflicts: []importIssue{},
	}
	skippedTotal, conflictTotal := 0, 0
	addSkipped := func(issue importIssue) {
		skippedTotal++
		if len(result.Skipped) < 100 {
			result.Skipped = append(result.Skipped, issue)
		}
	}
	addConflict := func(issue importIssue) {
		conflictTotal++
		if len(result.Conflicts) < 100 {
			result.Conflicts = append(result.Conflicts, issue)
		}
	}
	now := time.Now().UTC()
	documentIDs := map[string]string{}

	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		// sessions ---------------------------------------------------------
		for _, row := range backup.Sessions {
			var owner string
			err := tx.QueryRow(ctx, `select user_id::text from practice_sessions where id = $1::uuid`, row.ID).Scan(&owner)
			if err == nil {
				if owner != userID {
					addConflict(importIssue{Category: "sessions", ID: row.ID, Reason: "练习现场 ID 已属于其他账号，未导入。"})
				} else {
					addSkipped(importIssue{Category: "sessions", ID: row.ID, Reason: "已存在（幂等跳过）"})
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			status := row.Status
			if status == "active" {
				var activeExists bool
				if err := tx.QueryRow(ctx, `
					select exists (select 1 from practice_sessions where user_id = $1::uuid and status = 'active')`,
					userID).Scan(&activeExists); err != nil {
					return err
				}
				if activeExists {
					status = "finished"
					addConflict(importIssue{Category: "sessions", ID: row.ID, Reason: "当前账号已有活动练习现场，本条按已结束导入。"})
				}
			}
			scope, _ := json.Marshal(row.Scope)
			revisions, _ := json.Marshal(row.Revisions)
			createdAt := v2DateValue(row.CreatedAt, now)
			updatedAt := v2DateValue(row.UpdatedAt, createdAt)
			version := 1
			if row.Version != nil {
				version = *row.Version
			}
			if _, err := tx.Exec(ctx, `
				insert into practice_sessions
					(id, user_id, mode, scope, question_ids, revisions, index, elapsed_seconds, status,
					 bank_version, version, deadline_at, submitted_at, score, max_score, created_at, updated_at)
				values ($1::uuid, $2::uuid, $3, $4::jsonb, $5, $6::jsonb, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
				row.ID, userID, row.Mode, string(scope), row.QuestionIDs, string(revisions), *row.Index,
				*row.ElapsedSeconds, status, row.BankVersion, version,
				v2NullableDate(row.DeadlineAt), v2NullableDate(row.SubmittedAt),
				v2NullableInt(row.Score), v2NullableInt(row.MaxScore), createdAt, updatedAt); err != nil {
				return err
			}
			result.Imported["sessions"]++
		}

		// attempts ---------------------------------------------------------
		for _, row := range backup.Attempts {
			var owner string
			err := tx.QueryRow(ctx, `select user_id::text from attempts where id = $1`, row.ID).Scan(&owner)
			if err == nil {
				if owner != userID {
					addConflict(importIssue{Category: "attempts", ID: row.ID, Reason: "作答事件 ID 已属于其他账号，未导入。"})
				} else {
					addSkipped(importIssue{Category: "attempts", ID: row.ID, Reason: "已存在（幂等跳过）"})
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			revision, answer, currentRevision, found, err := loadQuestionForImport(ctx, tx, row.QuestionID)
			if err != nil {
				return err
			}
			if !found {
				addSkipped(importIssue{Category: "attempts", ID: row.ID, Reason: "题目不存在或已下线：" + row.QuestionID})
				continue
			}
			if row.QuestionRevision != "" && row.QuestionRevision != currentRevision {
				if historical, ok, err := loadHistoricalAnswer(ctx, tx, row.QuestionID, row.QuestionRevision); err != nil {
					return err
				} else if ok {
					revision, answer = row.QuestionRevision, historical
				} else {
					revision = currentRevision
					addConflict(importIssue{Category: "attempts", ID: row.ID,
						Reason: fmt.Sprintf("题目版本 %s 已不存在，按当前版本 %s 导入。", row.QuestionRevision, currentRevision)})
				}
			}
			correct := *row.Correct
			computed := row.Selected == answer
			if revision != row.QuestionRevision || row.QuestionRevision == "" {
				correct = computed
			} else if computed != correct {
				addConflict(importIssue{Category: "attempts", ID: row.ID,
					Reason: fmt.Sprintf("备份记录的正确性为 %v，与题目答案重算结果 %v 不一致；保留导出值。", correct, computed)})
			}
			var sessionArg any
			if row.SessionID != nil && *row.SessionID != "" {
				var sessionExists bool
				if err := tx.QueryRow(ctx, `
					select exists (select 1 from practice_sessions where id = $1::uuid and user_id = $2::uuid)`,
					*row.SessionID, userID).Scan(&sessionExists); err != nil {
					return err
				}
				if sessionExists {
					sessionArg = *row.SessionID
				} else {
					addConflict(importIssue{Category: "attempts", ID: row.ID,
						Reason: "关联练习现场不存在，按无现场导入。"})
				}
			}
			duration := int(*row.DurationMS)
			answeredAt, _ := time.Parse(time.RFC3339Nano, row.AnsweredAt)
			createdAt := v2DateValue(row.CreatedAt, answeredAt)
			if _, err := tx.Exec(ctx, `
				insert into attempts (id, user_id, session_id, question_id, question_revision, selected,
					correct, assisted, duration_ms, answered_at, client_seq, created_at)
				values ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9, $10, null, $11)`,
				row.ID, userID, sessionArg, row.QuestionID, revision, row.Selected,
				correct, *row.Assisted, duration, answeredAt, createdAt); err != nil {
				return err
			}
			result.Imported["attempts"]++
		}

		// marks ------------------------------------------------------------
		for _, row := range backup.Marks {
			exists, err := questionExists(ctx, tx, row.QuestionID)
			if err != nil {
				return err
			}
			if !exists {
				addSkipped(importIssue{Category: "marks", ID: row.QuestionID, Reason: "题目不存在或已下线：" + row.QuestionID})
				continue
			}
			var favorite, uncertain bool
			var note string
			var version int
			err = tx.QueryRow(ctx, `
				select favorite, uncertain, note, version from marks
				where user_id = $1::uuid and question_id = $2`, userID, row.QuestionID).
				Scan(&favorite, &uncertain, &note, &version)
			if err == nil {
				if favorite == *row.Favorite && uncertain == *row.Uncertain && note == *row.Note {
					addSkipped(importIssue{Category: "marks", ID: row.QuestionID, Reason: "已存在相同标记（幂等跳过）"})
				} else {
					addConflict(importIssue{Category: "marks", ID: row.QuestionID, Reason: "服务器已有不同标记，保留服务器版本。"})
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if _, err := tx.Exec(ctx, `
				insert into marks (user_id, question_id, favorite, uncertain, note, version, updated_at)
				values ($1::uuid, $2, $3, $4, $5, $6, $7)`,
				userID, row.QuestionID, *row.Favorite, *row.Uncertain, *row.Note,
				v2IntOrDefault(row.Version, 1), v2DateValue(row.UpdatedAt, now)); err != nil {
				return err
			}
			result.Imported["marks"]++
		}

		// reading ----------------------------------------------------------
		for _, row := range backup.Reading {
			var materialVersionID any
			var materialVersionText string
			err := tx.QueryRow(ctx, `select coalesce(current_version_id::text, '') from materials where id = $1`, row.MaterialID).
				Scan(&materialVersionText)
			if errors.Is(err, pgx.ErrNoRows) {
				addSkipped(importIssue{Category: "reading", ID: row.MaterialID, Reason: "资料不存在或已下线：" + row.MaterialID})
				continue
			}
			if err != nil {
				return err
			}
			if row.MaterialVersionID != nil && *row.MaterialVersionID != "" {
				if ok, err := rowExists(ctx, tx, "material_versions", *row.MaterialVersionID); err != nil {
					return err
				} else if ok {
					materialVersionID = *row.MaterialVersionID
				} else {
					addConflict(importIssue{Category: "reading", ID: row.MaterialID, Reason: "资料版本不存在，按当前版本记录。"})
				}
			}
			if materialVersionID == nil && materialVersionText != "" {
				materialVersionID = materialVersionText
			}
			sectionAnchor := v2String(row.SectionAnchor)
			offset := 0
			if row.SectionOffset != nil {
				offset = *row.SectionOffset
			}
			var existingProgress float64
			var existingDone bool
			err = tx.QueryRow(ctx, `
				select progress::float8, done from reading_progress
				where user_id = $1::uuid and material_id = $2`, userID, row.MaterialID).
				Scan(&existingProgress, &existingDone)
			if err == nil {
				if existingProgress >= *row.Progress && (existingDone || !*row.Done) {
					addSkipped(importIssue{Category: "reading", ID: row.MaterialID, Reason: "服务器进度不落后于备份（幂等跳过）"})
					continue
				}
				if _, err := tx.Exec(ctx, `
					update reading_progress set
						progress = greatest(progress, $3),
						material_version_id = coalesce(material_version_id, $4::uuid),
						section_anchor = case when $3 >= progress then $5 else section_anchor end,
						section_offset = case when $3 >= progress then $6 else section_offset end,
						done = done or $7,
						updated_at = $8
					where user_id = $1::uuid and material_id = $2`,
					userID, row.MaterialID, *row.Progress, materialVersionID, sectionAnchor, offset,
					*row.Done, v2DateValue(row.UpdatedAt, now)); err != nil {
					return err
				}
				result.Imported["reading"]++
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if _, err := tx.Exec(ctx, `
				insert into reading_progress (user_id, material_id, material_version_id, progress,
					section_anchor, section_offset, done, updated_at)
				values ($1::uuid, $2, $3::uuid, $4, $5, $6, $7, $8)`,
				userID, row.MaterialID, materialVersionID, *row.Progress, sectionAnchor, offset,
				*row.Done, v2DateValue(row.UpdatedAt, now)); err != nil {
				return err
			}
			result.Imported["reading"]++
		}

		// annotations ------------------------------------------------------
		for _, row := range backup.Annotations {
			var owner string
			err := tx.QueryRow(ctx, `select user_id::text from annotations where id = $1::uuid`, row.ID).Scan(&owner)
			if err == nil {
				if owner != userID {
					addConflict(importIssue{Category: "annotations", ID: row.ID, Reason: "批注 ID 已属于其他账号，未导入。"})
				} else {
					addSkipped(importIssue{Category: "annotations", ID: row.ID, Reason: "已存在（幂等跳过）"})
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			exists, err := rowExists(ctx, tx, "materials", row.MaterialID)
			if err != nil {
				return err
			}
			if !exists {
				addSkipped(importIssue{Category: "annotations", ID: row.ID, Reason: "资料不存在或已下线：" + row.MaterialID})
				continue
			}
			var versionArg any
			if row.MaterialVersionID != nil && *row.MaterialVersionID != "" {
				ok, err := rowExists(ctx, tx, "material_versions", *row.MaterialVersionID)
				if err != nil {
					return err
				}
				if ok {
					versionArg = *row.MaterialVersionID
				} else {
					addConflict(importIssue{Category: "annotations", ID: row.ID, Reason: "资料版本不存在，批注不绑定版本。"})
				}
			}
			createdAt := v2DateValue(row.CreatedAt, now)
			if _, err := tx.Exec(ctx, `
				insert into annotations (id, user_id, material_id, material_version_id, section_anchor,
					quote, note, created_at, updated_at, deleted_at)
				values ($1::uuid, $2::uuid, $3, $4::uuid, $5, $6, $7, $8, $9, $10)`,
				row.ID, userID, row.MaterialID, versionArg, v2String(row.SectionAnchor),
				v2String(row.Quote), v2String(row.Note), createdAt,
				v2DateValue(row.UpdatedAt, createdAt), v2NullableDate(row.DeletedAt)); err != nil {
				return err
			}
			result.Imported["annotations"]++
		}

		// writing documents ------------------------------------------------
		for _, row := range backup.WritingDocuments {
			exists, err := rowExistsPair(ctx, tx, "subjective_items", row.TargetID, row.Kind)
			if err != nil {
				return err
			}
			if !exists {
				addSkipped(importIssue{Category: "writingDocuments", ID: row.ID, Reason: "写作题目不存在：" + row.TargetID})
				continue
			}
			values, _ := json.Marshal(row.Values)
			var existingID string
			var existingVersion int
			var existingValues []byte
			err = tx.QueryRow(ctx, `
				select id::text, version, values from writing_documents
				where user_id = $1::uuid and kind = $2 and target_id = $3`, userID, row.Kind, row.TargetID).
				Scan(&existingID, &existingVersion, &existingValues)
			if errors.Is(err, pgx.ErrNoRows) {
				createdAt := v2DateValue(row.CreatedAt, now)
				version := v2IntOrDefault(row.Version, 1)
				if _, err := tx.Exec(ctx, `
					insert into writing_documents (id, user_id, kind, target_id, values, version, created_at, updated_at)
					values ($1::uuid, $2::uuid, $3, $4, $5::jsonb, $6, $7, $8)`,
					row.ID, userID, row.Kind, row.TargetID, string(values), version,
					createdAt, v2DateValue(row.UpdatedAt, createdAt)); err != nil {
					return err
				}
				documentIDs[row.ID] = row.ID
				result.Imported["writingDocuments"]++
				continue
			}
			if err != nil {
				return err
			}
			documentIDs[row.ID] = existingID
			exportedVersion := v2IntOrDefault(row.Version, 1)
			switch {
			case existingVersion > exportedVersion:
				addConflict(importIssue{Category: "writingDocuments", ID: row.ID,
					Reason: fmt.Sprintf("服务器草稿版本 %d 更新于备份版本 %d，保留服务器版本并保留备份副本。", existingVersion, exportedVersion)})
				preserveConflictVersion(ctx, tx, existingID, userID, exportedVersion, values, now)
			case existingVersion == exportedVersion:
				if jsonValuesEqual(existingValues, values) {
					addSkipped(importIssue{Category: "writingDocuments", ID: row.ID, Reason: "草稿已存在且版本内容一致（幂等跳过）"})
				} else {
					addConflict(importIssue{Category: "writingDocuments", ID: row.ID,
						Reason: "版本号相同但内容不同，保留服务器版本并保留备份副本。"})
					preserveConflictVersion(ctx, tx, existingID, userID, exportedVersion, values, now)
				}
			default:
				// Restoring an older account: the exported document is newer than
				// what the server holds, so the export wins and both versions stay.
				if _, err := tx.Exec(ctx, `
					update writing_documents set values = $2::jsonb, version = $3, updated_at = $4
					where id = $1::uuid`, existingID, string(values), exportedVersion,
					v2DateValue(row.UpdatedAt, now)); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `
					insert into writing_versions (document_id, version, values, source, created_by, created_at)
					values ($1::uuid, $2, $3::jsonb, 'user', $4::uuid, $5)
					on conflict do nothing`,
					existingID, exportedVersion, string(values), userID, v2DateValue(row.UpdatedAt, now)); err != nil {
					return err
				}
				result.Imported["writingDocuments"]++
			}
		}

		// writing versions -------------------------------------------------
		for _, row := range backup.WritingVersions {
			var versionID string
			var versionOwner string
			err := tx.QueryRow(ctx, `
				select v.id::text, d.user_id::text from writing_versions v
				join writing_documents d on d.id = v.document_id
				where v.id = $1::uuid`, row.ID).Scan(&versionID, &versionOwner)
			if err == nil {
				if versionOwner != userID {
					addConflict(importIssue{Category: "writingVersions", ID: row.ID, Reason: "草稿版本 ID 已属于其他账号，未导入。"})
				} else {
					addSkipped(importIssue{Category: "writingVersions", ID: row.ID, Reason: "已存在（幂等跳过）"})
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			documentID, ok := documentIDs[row.DocumentID]
			if !ok {
				addSkipped(importIssue{Category: "writingVersions", ID: row.ID, Reason: "关联草稿未导入或不存在：" + row.DocumentID})
				continue
			}
			values, _ := json.Marshal(row.Values)
			createdAt := v2DateValue(row.CreatedAt, now)
			if _, err := tx.Exec(ctx, `
				insert into writing_versions (id, document_id, version, values, source, created_by, created_at)
				values ($1::uuid, $2::uuid, $3, $4::jsonb, $5, $6::uuid, $7)
				on conflict do nothing`,
				row.ID, documentID, v2IntOrDefault(row.Version, 1), string(values), row.Source, userID, createdAt); err != nil {
				return err
			}
			result.Imported["writingVersions"]++
		}

		// writing reviews --------------------------------------------------
		for _, row := range backup.WritingReviews {
			var owner string
			err := tx.QueryRow(ctx, `
				select d.user_id::text from writing_reviews r
				join writing_documents d on d.id = r.document_id
				where r.id = $1::uuid`, row.ID).Scan(&owner)
			if err == nil {
				if owner != userID {
					addConflict(importIssue{Category: "writingReviews", ID: row.ID, Reason: "点评 ID 已属于其他账号，未导入。"})
				} else {
					addSkipped(importIssue{Category: "writingReviews", ID: row.ID, Reason: "已存在（幂等跳过）"})
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			documentID, ok := documentIDs[row.DocumentID]
			if !ok {
				addSkipped(importIssue{Category: "writingReviews", ID: row.ID, Reason: "关联草稿未导入或不存在：" + row.DocumentID})
				continue
			}
			var feedback any
			if len(row.Feedback) > 0 && string(row.Feedback) != "null" {
				feedback = string(row.Feedback)
			}
			createdAt := v2DateValue(row.CreatedAt, now)
			if _, err := tx.Exec(ctx, `
				insert into writing_reviews (id, document_id, document_version, task_id, status, feedback, created_at, completed_at)
				values ($1::uuid, $2::uuid, $3, null, $4, $5::jsonb, $6, $7)`,
				row.ID, documentID, v2IntOrDefault(row.DocumentVersion, 1), row.Status, feedback,
				createdAt, v2NullableDate(row.CompletedAt)); err != nil {
				return err
			}
			result.Imported["writingReviews"]++
		}

		// evidence ---------------------------------------------------------
		for _, row := range backup.Evidence {
			var owner string
			err := tx.QueryRow(ctx, `select user_id::text from evidence where id = $1::uuid`, row.ID).Scan(&owner)
			if err == nil {
				if owner != userID {
					addConflict(importIssue{Category: "evidence", ID: row.ID, Reason: "证据 ID 已属于其他账号，未导入。"})
				} else {
					addSkipped(importIssue{Category: "evidence", ID: row.ID, Reason: "已存在（幂等跳过）"})
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			questionArg, err := v2ExistingOrNil(ctx, tx, "questions", row.QuestionID)
			if err != nil {
				return err
			}
			knowledgeArg, err := v2ExistingOrNil(ctx, tx, "knowledge_points", row.KnowledgePointID)
			if err != nil {
				return err
			}
			materialArg, err := v2ExistingOrNil(ctx, tx, "materials", row.MaterialID)
			if err != nil {
				return err
			}
			if row.QuestionID != nil && questionArg == nil {
				addConflict(importIssue{Category: "evidence", ID: row.ID, Reason: "题目不存在，证据保留但不关联题目。"})
			}
			occurredAt, _ := time.Parse(time.RFC3339Nano, row.OccurredAt)
			payload, _ := json.Marshal(row.Payload)
			if _, err := tx.Exec(ctx, `
				insert into evidence (id, user_id, kind, question_id, knowledge_point_id, material_id,
					rule_version, payload, occurred_at, created_at)
				values ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8::jsonb, $9, $10)`,
				row.ID, userID, row.Kind, questionArg, knowledgeArg, materialArg,
				v2StringOrDefault(row.RuleVersion, "v1"), string(payload), occurredAt,
				v2DateValue(row.CreatedAt, occurredAt)); err != nil {
				return err
			}
			result.Imported["evidence"]++
		}

		// mastery ----------------------------------------------------------
		for _, row := range backup.Mastery {
			exists, err := rowExists(ctx, tx, "knowledge_points", row.KnowledgePointID)
			if err != nil {
				return err
			}
			if !exists {
				addSkipped(importIssue{Category: "mastery", ID: row.KnowledgePointID, Reason: "知识点不存在或已下线：" + row.KnowledgePointID})
				continue
			}
			var existingState string
			err = tx.QueryRow(ctx, `
				select state from mastery where user_id = $1::uuid and knowledge_point_id = $2`,
				userID, row.KnowledgePointID).Scan(&existingState)
			if errors.Is(err, pgx.ErrNoRows) {
				if _, err := tx.Exec(ctx, `
					insert into mastery (user_id, knowledge_point_id, state, evidence_count, rule_version, updated_at)
					values ($1::uuid, $2, $3, $4, $5, $6)`,
					userID, row.KnowledgePointID, row.State, v2IntOrDefault(row.EvidenceCount, 0),
					v2StringOrDefault(row.RuleVersion, "v1"), v2DateValue(row.UpdatedAt, now)); err != nil {
					return err
				}
				result.Imported["mastery"]++
				continue
			}
			if err != nil {
				return err
			}
			if masteryRank(row.State) > masteryRank(existingState) {
				if _, err := tx.Exec(ctx, `
					update mastery set state = $3, evidence_count = $4, rule_version = $5, updated_at = $6
					where user_id = $1::uuid and knowledge_point_id = $2`,
					userID, row.KnowledgePointID, row.State, v2IntOrDefault(row.EvidenceCount, 0),
					v2StringOrDefault(row.RuleVersion, "v1"), v2DateValue(row.UpdatedAt, now)); err != nil {
					return err
				}
				result.Imported["mastery"]++
			} else {
				addSkipped(importIssue{Category: "mastery", ID: row.KnowledgePointID, Reason: "已有同等或更强的掌握证据（保留服务器版本）"})
			}
		}

		// review items -----------------------------------------------------
		for _, row := range backup.ReviewItems {
			exists, err := questionExists(ctx, tx, row.QuestionID)
			if err != nil {
				return err
			}
			if !exists {
				addSkipped(importIssue{Category: "reviewItems", ID: row.QuestionID, Reason: "题目不存在或已下线：" + row.QuestionID})
				continue
			}
			dueAt, _ := time.Parse(time.RFC3339Nano, row.DueAt)
			updatedAt := v2DateValue(row.UpdatedAt, now)
			reason, _ := json.Marshal(row.Reason)
			var existingUpdated time.Time
			err = tx.QueryRow(ctx, `
				select updated_at from review_items where user_id = $1::uuid and question_id = $2`,
				userID, row.QuestionID).Scan(&existingUpdated)
			if errors.Is(err, pgx.ErrNoRows) {
				if _, err := tx.Exec(ctx, `
					insert into review_items (user_id, question_id, due_at, interval_days, reason, updated_at)
					values ($1::uuid, $2, $3, $4, $5::jsonb, $6)`,
					userID, row.QuestionID, dueAt, v2IntOrDefault(row.IntervalDays, 1), string(reason), updatedAt); err != nil {
					return err
				}
				result.Imported["reviewItems"]++
				continue
			}
			if err != nil {
				return err
			}
			if updatedAt.After(existingUpdated) {
				if _, err := tx.Exec(ctx, `
					update review_items set due_at = $3, interval_days = $4, reason = $5::jsonb, updated_at = $6
					where user_id = $1::uuid and question_id = $2`,
					userID, row.QuestionID, dueAt, v2IntOrDefault(row.IntervalDays, 1), string(reason), updatedAt); err != nil {
					return err
				}
				result.Imported["reviewItems"]++
			} else {
				addSkipped(importIssue{Category: "reviewItems", ID: row.QuestionID, Reason: "服务器复习安排更新（保留服务器版本）"})
			}
		}

		// teaching identity ------------------------------------------------
		for _, row := range backup.TeachingIdentity {
			var existing string
			err := tx.QueryRow(ctx, `
				select teaching_user_id from teaching_identities where user_id = $1::uuid`, userID).Scan(&existing)
			if err == nil {
				if existing == row.TeachingUserID {
					addSkipped(importIssue{Category: "teachingIdentity", ID: row.TeachingUserID, Reason: "已存在（幂等跳过）"})
				} else {
					addConflict(importIssue{Category: "teachingIdentity", ID: row.TeachingUserID, Reason: "账号已有不同教学身份，保留服务器版本。"})
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			var usedByOther bool
			if err := tx.QueryRow(ctx, `
				select exists (select 1 from teaching_identities where teaching_user_id = $1)`,
				row.TeachingUserID).Scan(&usedByOther); err != nil {
				return err
			}
			if usedByOther {
				addConflict(importIssue{Category: "teachingIdentity", ID: row.TeachingUserID, Reason: "教学身份已绑定其他账号，未导入。"})
				continue
			}
			if _, err := tx.Exec(ctx, `
				insert into teaching_identities (user_id, teaching_user_id, credential_ciphertext, created_at, rotated_at)
				values ($1::uuid, $2, '', $3, $4)`,
				userID, row.TeachingUserID, v2DateValue(row.CreatedAt, now), v2NullableDate(row.RotatedAt)); err != nil {
				return err
			}
			result.Imported["teachingIdentity"]++
		}

		// AI tasks ---------------------------------------------------------
		for _, row := range backup.AITasks {
			var owner string
			err := tx.QueryRow(ctx, `select user_id::text from ai_tasks where id = $1::uuid`, row.ID).Scan(&owner)
			if err == nil {
				if owner != userID {
					addConflict(importIssue{Category: "aiTasks", ID: row.ID, Reason: "AI 任务 ID 已属于其他账号，未导入。"})
				} else {
					addSkipped(importIssue{Category: "aiTasks", ID: row.ID, Reason: "已存在（幂等跳过）"})
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			contextJSON, _ := json.Marshal(row.Context)
			createdAt := v2DateValue(row.CreatedAt, now)
			if _, err := tx.Exec(ctx, `
				insert into ai_tasks (id, user_id, kind, status, context, idempotency_key, attempt,
					error_code, error_message, created_at, started_at, finished_at)
				values ($1::uuid, $2::uuid, $3, $4, $5::jsonb, $6, $7, $8, $9, $10, $11, $12)`,
				row.ID, userID, row.Kind, row.Status, string(contextJSON), "v2-import:"+row.ID,
				v2IntOrDefault(row.Attempt, 0), v2String(row.ErrorCode), v2String(row.ErrorMessage),
				createdAt, v2NullableDate(row.StartedAt), v2NullableDate(row.FinishedAt)); err != nil {
				return err
			}
			result.Imported["aiTasks"]++
		}

		// AI events --------------------------------------------------------
		for _, row := range backup.AIEvents {
			var taskOwner string
			err := tx.QueryRow(ctx, `select user_id::text from ai_tasks where id = $1::uuid`, row.TaskID).Scan(&taskOwner)
			if errors.Is(err, pgx.ErrNoRows) {
				addSkipped(importIssue{Category: "aiEvents", ID: fmt.Sprintf("%s#%d", row.TaskID, v2IntOrDefault(row.Seq, 0)), Reason: "关联 AI 任务未导入或不存在。"})
				continue
			}
			if err != nil {
				return err
			}
			if taskOwner != userID {
				addConflict(importIssue{Category: "aiEvents", ID: fmt.Sprintf("%s#%d", row.TaskID, v2IntOrDefault(row.Seq, 0)), Reason: "关联 AI 任务属于其他账号，未导入。"})
				continue
			}
			payload, _ := json.Marshal(row.Payload)
			tag, err := tx.Exec(ctx, `
				insert into ai_events (task_id, seq, type, payload, created_at)
				values ($1::uuid, $2, $3, $4::jsonb, $5)
				on conflict (task_id, seq) do nothing`,
				row.TaskID, v2IntOrDefault(row.Seq, 1), row.Type, string(payload), v2DateValue(row.CreatedAt, now))
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				result.Imported["aiEvents"]++
			} else {
				addSkipped(importIssue{Category: "aiEvents", ID: fmt.Sprintf("%s#%d", row.TaskID, v2IntOrDefault(row.Seq, 0)), Reason: "已存在（幂等跳过）"})
			}
		}

		// AI messages ------------------------------------------------------
		for _, row := range backup.AIMessages {
			var messageOwner string
			err := tx.QueryRow(ctx, `
				select t.user_id::text from ai_messages m join ai_tasks t on t.id = m.task_id
				where m.id = $1::uuid`, row.ID).Scan(&messageOwner)
			if err == nil {
				if messageOwner != userID {
					addConflict(importIssue{Category: "aiMessages", ID: row.ID, Reason: "AI 消息 ID 已属于其他账号，未导入。"})
				} else {
					addSkipped(importIssue{Category: "aiMessages", ID: row.ID, Reason: "已存在（幂等跳过）"})
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			var taskOwner string
			err = tx.QueryRow(ctx, `select user_id::text from ai_tasks where id = $1::uuid`, row.TaskID).Scan(&taskOwner)
			if errors.Is(err, pgx.ErrNoRows) {
				addSkipped(importIssue{Category: "aiMessages", ID: row.ID, Reason: "关联 AI 任务未导入或不存在。"})
				continue
			}
			if err != nil {
				return err
			}
			if taskOwner != userID {
				addConflict(importIssue{Category: "aiMessages", ID: row.ID, Reason: "关联 AI 任务属于其他账号，未导入。"})
				continue
			}
			if _, err := tx.Exec(ctx, `
				insert into ai_messages (id, task_id, role, content, created_at)
				values ($1::uuid, $2::uuid, $3, $4, $5)`,
				row.ID, row.TaskID, row.Role, row.Content, v2DateValue(row.CreatedAt, now)); err != nil {
				return err
			}
			result.Imported["aiMessages"]++
		}

		// imported legacy records ------------------------------------------
		for _, row := range backup.ImportedRecords {
			var owner string
			err := tx.QueryRow(ctx, `select user_id::text from imported_records where id = $1::uuid`, row.ID).Scan(&owner)
			if err == nil {
				if owner != userID {
					addConflict(importIssue{Category: "importedRecords", ID: row.ID, Reason: "存档 ID 已属于其他账号，未导入。"})
				} else {
					addSkipped(importIssue{Category: "importedRecords", ID: row.ID, Reason: "已存在（幂等跳过）"})
				}
				continue
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			payload, _ := json.Marshal(row.Payload)
			createdAt := v2DateValue(row.CreatedAt, now)
			tag, err := tx.Exec(ctx, `
				insert into imported_records (id, user_id, source, kind, external_id, payload, note, created_at)
				values ($1::uuid, $2::uuid, $3, $4, $5, $6::jsonb, $7, $8)
				on conflict (user_id, source, kind, external_id) do nothing`,
				row.ID, userID, row.Source, row.Kind, row.ExternalID, string(payload),
				v2String(row.Note), createdAt)
			if err != nil {
				return err
			}
			if tag.RowsAffected() > 0 {
				result.Imported["importedRecords"]++
			} else {
				addSkipped(importIssue{Category: "importedRecords", ID: row.ID, Reason: "已存在（幂等跳过）"})
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

// preserveConflictVersion keeps the exported draft content next to the newer
// server draft instead of silently discarding it.
func preserveConflictVersion(ctx context.Context, tx pgx.Tx, documentID, userID string, version int, values []byte, now time.Time) {
	_, _ = tx.Exec(ctx, `
		insert into writing_versions (document_id, version, values, source, created_by, created_at)
		values ($1::uuid, $2, $3::jsonb, 'conflict', $4::uuid, $5)
		on conflict do nothing`,
		documentID, version, string(values), userID, now)
}

func v2String(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func v2StringOrDefault(value *string, fallback string) string {
	if value == nil || *value == "" {
		return fallback
	}
	return *value
}

func v2IntOrDefault(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func v2NullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func v2NullableDate(value *string) any {
	if value == nil || *value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return nil
	}
	return parsed
}

func v2DateValue(value *string, fallback time.Time) time.Time {
	if value == nil || *value == "" {
		return fallback
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return fallback
	}
	return parsed
}

func jsonValuesEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, _ := json.Marshal(leftValue)
	rightCanonical, _ := json.Marshal(rightValue)
	return string(leftCanonical) == string(rightCanonical)
}

// rowExists reports whether a row with a text primary key exists. It is only
// called with constant table names.
func rowExists(ctx context.Context, tx pgx.Tx, table, id string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `select exists (select 1 from "`+table+`" where id = $1)`, id).Scan(&exists)
	return exists, err
}

// rowExistsPair checks a (id, kind) pair in subjective_items.
func rowExistsPair(ctx context.Context, tx pgx.Tx, table, id, second string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `select exists (select 1 from "`+table+`" where id = $1 and kind = $2)`, id, second).Scan(&exists)
	return exists, err
}

// v2ExistingOrNil returns the id when the referenced row exists, otherwise nil
// so an optional foreign key does not block restoring the fact itself.
func v2ExistingOrNil(ctx context.Context, tx pgx.Tx, table string, id *string) (any, error) {
	if id == nil || *id == "" {
		return nil, nil
	}
	exists, err := rowExists(ctx, tx, table, *id)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return *id, nil
}

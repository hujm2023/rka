package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"rka/internal/learning"
)

// querier is the common subset of *pgxpool.Pool and pgx.Tx. Derivation helpers
// accept it so the same SQL runs on read paths and inside the self-declaration
// transaction.
type querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ---------------------------------------------------------------- raw facts

// attemptFact is one immutable answer event. The evidence package never writes
// this table; practice does.
type attemptFact struct {
	ID         string
	QuestionID string
	Revision   string
	SessionID  string
	Correct    bool
	Assisted   bool
	DurationMS int
	AnsweredAt time.Time
}

func loadAttempts(ctx context.Context, q querier, userID string) ([]attemptFact, error) {
	rows, err := q.Query(ctx, `
		select a.id, a.question_id, a.question_revision, coalesce(a.session_id::text, ''),
		       a.correct, a.assisted, a.duration_ms, a.answered_at
		from released_attempts a
		where a.user_id = $1::uuid
		order by a.answered_at desc, a.created_at desc`, userID)
	if err != nil {
		return nil, fmt.Errorf("load attempts: %w", err)
	}
	defer rows.Close()
	facts := []attemptFact{}
	for rows.Next() {
		var fact attemptFact
		if err := rows.Scan(&fact.ID, &fact.QuestionID, &fact.Revision, &fact.SessionID,
			&fact.Correct, &fact.Assisted, &fact.DurationMS, &fact.AnsweredAt); err != nil {
			return nil, fmt.Errorf("scan attempt: %w", err)
		}
		facts = append(facts, fact)
	}
	return facts, rows.Err()
}

// evidenceFact is one append-only evidence row owned by this package (reading,
// hint usage or self-declaration).
type evidenceFact struct {
	ID                string
	Kind              learning.EvidenceKind
	QuestionID        string
	KnowledgePointID  string
	MaterialID        string
	MaterialVersionID string
	Payload           json.RawMessage
	OccurredAt        time.Time
}

func loadEvidence(ctx context.Context, q querier, userID string, kinds []learning.EvidenceKind) ([]evidenceFact, error) {
	values := make([]string, len(kinds))
	for i, kind := range kinds {
		values[i] = string(kind)
	}
	rows, err := q.Query(ctx, `
		select e.id::text, e.kind, coalesce(e.question_id, ''), coalesce(e.knowledge_point_id, ''),
		       coalesce(e.material_id, ''), coalesce(e.material_version_id::text, ''), e.payload, e.occurred_at
		from evidence e
		where e.user_id = $1::uuid and e.kind = any($2::text[])
		order by e.occurred_at desc, e.created_at desc`, userID, values)
	if err != nil {
		return nil, fmt.Errorf("load evidence: %w", err)
	}
	defer rows.Close()
	facts := []evidenceFact{}
	for rows.Next() {
		var fact evidenceFact
		var kind string
		if err := rows.Scan(&fact.ID, &kind, &fact.QuestionID, &fact.KnowledgePointID,
			&fact.MaterialID, &fact.MaterialVersionID, &fact.Payload, &fact.OccurredAt); err != nil {
			return nil, fmt.Errorf("scan evidence: %w", err)
		}
		fact.Kind = learning.EvidenceKind(kind)
		facts = append(facts, fact)
	}
	return facts, rows.Err()
}

type readingPayload struct {
	MaterialVersionID string  `json:"materialVersionId"`
	Progress          float64 `json:"progress"`
	SectionAnchor     string  `json:"sectionAnchor"`
	SectionKey        string  `json:"sectionKey"`
	Offset            int     `json:"offset"`
	Done              bool    `json:"done"`
}

type selfDeclaredPayload struct {
	Mastered bool `json:"mastered"`
}

func (e evidenceFact) reading() readingPayload {
	var payload readingPayload
	_ = json.Unmarshal(e.Payload, &payload)
	return payload
}

func (e evidenceFact) selfDeclared() selfDeclaredPayload {
	var payload selfDeclaredPayload
	_ = json.Unmarshal(e.Payload, &payload)
	return payload
}

type readingProgressRow struct {
	MaterialID        string
	MaterialVersionID string
	Progress          float64
	SectionAnchor     string
	SectionOffset     int
	Done              bool
	UpdatedAt         time.Time
}

func loadReadingProgress(ctx context.Context, q querier, userID string) ([]readingProgressRow, error) {
	rows, err := q.Query(ctx, `
		select material_id, coalesce(material_version_id::text, ''), progress,
		       section_anchor, section_offset, done, updated_at
		from reading_progress
		where user_id = $1::uuid
		order by updated_at desc`, userID)
	if err != nil {
		return nil, fmt.Errorf("load reading progress: %w", err)
	}
	defer rows.Close()
	items := []readingProgressRow{}
	for rows.Next() {
		var item readingProgressRow
		if err := rows.Scan(&item.MaterialID, &item.MaterialVersionID, &item.Progress,
			&item.SectionAnchor, &item.SectionOffset, &item.Done, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan reading progress: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ---------------------------------------------------------------- summary

// evidenceRef is a public, lookup-friendly reference to one learning fact.
// Every item carries the id of its source row plus enough identifiers to fetch
// the original question, material version or knowledge point.
type evidenceRef struct {
	ID                string                `json:"id"`
	Kind              learning.EvidenceKind `json:"kind"`
	Source            string                `json:"source"`
	OccurredAt        time.Time             `json:"occurredAt"`
	QuestionID        string                `json:"questionId,omitempty"`
	QuestionRevision  string                `json:"questionRevision,omitempty"`
	SessionID         string                `json:"sessionId,omitempty"`
	MaterialID        string                `json:"materialId,omitempty"`
	MaterialVersionID string                `json:"materialVersionId,omitempty"`
	SectionAnchor     string                `json:"sectionAnchor,omitempty"`
	SectionKey        string                `json:"sectionKey,omitempty"`
	KnowledgePointID  string                `json:"knowledgePointId,omitempty"`
	Correct           *bool                 `json:"correct,omitempty"`
	Assisted          *bool                 `json:"assisted,omitempty"`
	DurationMS        int                   `json:"durationMs,omitempty"`
	Mastered          *bool                 `json:"mastered,omitempty"`
}

func boolPointer(value bool) *bool { return &value }

func attemptRef(fact attemptFact) evidenceRef {
	return evidenceRef{
		ID:               fact.ID,
		Kind:             learning.EvidenceKindOf(fact.Correct, fact.Assisted),
		Source:           "attempt",
		OccurredAt:       fact.AnsweredAt,
		QuestionID:       fact.QuestionID,
		QuestionRevision: fact.Revision,
		SessionID:        fact.SessionID,
		Correct:          boolPointer(fact.Correct),
		Assisted:         boolPointer(fact.Assisted),
		DurationMS:       fact.DurationMS,
	}
}

func evidenceRefOf(fact evidenceFact) evidenceRef {
	ref := evidenceRef{
		ID:               fact.ID,
		Kind:             fact.Kind,
		Source:           "evidence",
		OccurredAt:       fact.OccurredAt,
		QuestionID:       fact.QuestionID,
		KnowledgePointID: fact.KnowledgePointID,
		MaterialID:       fact.MaterialID,
	}
	switch fact.Kind {
	case learning.KindReading:
		payload := fact.reading()
		ref.MaterialVersionID = fact.MaterialVersionID
		if ref.MaterialVersionID == "" {
			ref.MaterialVersionID = payload.MaterialVersionID
		}
		ref.SectionAnchor = payload.SectionAnchor
		ref.SectionKey = payload.SectionKey
	case learning.KindSelfDeclared:
		ref.Mastered = boolPointer(fact.selfDeclared().Mastered)
	}
	return ref
}

type readingProgress struct {
	MaterialID        string    `json:"materialId"`
	MaterialVersionID string    `json:"materialVersionId,omitempty"`
	Progress          float64   `json:"progress"`
	SectionAnchor     string    `json:"sectionAnchor,omitempty"`
	SectionOffset     int       `json:"sectionOffset"`
	Done              bool      `json:"done"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type summaryData struct {
	Counts          map[string]int    `json:"counts"`
	Recent          []evidenceRef     `json:"recent"`
	ReadingProgress []readingProgress `json:"readingProgress"`
}

func newCounts() map[string]int {
	return map[string]int{
		string(learning.KindReading):         0,
		string(learning.KindHintUsed):        0,
		string(learning.KindAssistedPass):    0,
		string(learning.KindIndependentPass): 0,
		string(learning.KindWrong):           0,
		string(learning.KindSelfDeclared):    0,
	}
}

func (s *Service) buildSummary(ctx context.Context, userID string, recentLimit int) (summaryData, error) {
	attempts, err := loadAttempts(ctx, s.db.Pool, userID)
	if err != nil {
		return summaryData{}, err
	}
	evidenceRows, err := loadEvidence(ctx, s.db.Pool, userID, []learning.EvidenceKind{
		learning.KindReading, learning.KindHintUsed, learning.KindSelfDeclared,
	})
	if err != nil {
		return summaryData{}, err
	}
	progressRows, err := loadReadingProgress(ctx, s.db.Pool, userID)
	if err != nil {
		return summaryData{}, err
	}

	counts := newCounts()
	refs := make([]evidenceRef, 0, len(attempts)+len(evidenceRows))
	for _, fact := range attempts {
		counts[string(learning.EvidenceKindOf(fact.Correct, fact.Assisted))]++
		refs = append(refs, attemptRef(fact))
	}
	// "阅读" counts distinct materials the learner has started, mirroring the
	// reading-progress state rather than the number of scroll reports.
	// reading_updates keeps the raw report count visible without inflating it.
	startedReading := map[string]bool{}
	readingUpdates := 0
	for _, fact := range evidenceRows {
		if fact.Kind == learning.KindReading {
			readingUpdates++
			payload := fact.reading()
			if fact.MaterialID != "" && (payload.Done || payload.Progress > 0) {
				startedReading[fact.MaterialID] = true
			}
		} else {
			counts[string(fact.Kind)]++
		}
		refs = append(refs, evidenceRefOf(fact))
	}
	for _, row := range progressRows {
		if row.Done || row.Progress > 0 {
			startedReading[row.MaterialID] = true
		}
	}
	counts[string(learning.KindReading)] = len(startedReading)
	counts["reading_updates"] = readingUpdates
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].OccurredAt.Equal(refs[j].OccurredAt) {
			return refs[i].ID > refs[j].ID
		}
		return refs[i].OccurredAt.After(refs[j].OccurredAt)
	})
	if len(refs) > recentLimit {
		refs = refs[:recentLimit]
	}

	progress := make([]readingProgress, 0, len(progressRows))
	for _, row := range progressRows {
		progress = append(progress, readingProgress{
			MaterialID:        row.MaterialID,
			MaterialVersionID: row.MaterialVersionID,
			Progress:          row.Progress,
			SectionAnchor:     row.SectionAnchor,
			SectionOffset:     row.SectionOffset,
			Done:              row.Done,
			UpdatedAt:         row.UpdatedAt,
		})
	}
	return summaryData{Counts: counts, Recent: refs, ReadingProgress: progress}, nil
}

// ---------------------------------------------------------------- mastery

// kpEvidence accumulates the evidence kinds seen for one knowledge point.
type kpEvidence struct {
	kinds  map[learning.EvidenceKind]map[string]struct{}
	counts map[string]int
	last   time.Time
}

func newKPEvidence() *kpEvidence {
	return &kpEvidence{
		kinds:  map[learning.EvidenceKind]map[string]struct{}{},
		counts: newCounts(),
	}
}

// mark registers that a kind is present without counting another fact.
func (e *kpEvidence) mark(kind learning.EvidenceKind, key string) {
	if e.kinds[kind] == nil {
		e.kinds[kind] = map[string]struct{}{}
	}
	e.kinds[kind][key] = struct{}{}
}

func (e *kpEvidence) count(kind learning.EvidenceKind) {
	e.counts[string(kind)]++
}

// add records one distinct fact and its timestamp.
func (e *kpEvidence) add(kind learning.EvidenceKind, key string, at time.Time) {
	e.mark(kind, key)
	e.count(kind)
	if at.After(e.last) {
		e.last = at
	}
}

func (e *kpEvidence) hasKindKey(kind learning.EvidenceKind, key string) bool {
	_, ok := e.kinds[kind][key]
	return ok
}

func (e *kpEvidence) state() learning.MasteryState {
	kinds := make([]learning.EvidenceKind, 0, len(e.kinds))
	for kind := range e.kinds {
		kinds = append(kinds, kind)
	}
	return learning.StateFromEvidence(kinds)
}

func (e *kpEvidence) evidenceCount() int {
	total := 0
	for _, count := range e.counts {
		total += count
	}
	return total
}

func evidenceDedupeKey(fact evidenceFact) string {
	if fact.Kind == learning.KindReading && fact.MaterialID != "" {
		return "material:" + fact.MaterialID
	}
	return "evidence:" + fact.ID
}

// buildMastery recomputes per-knowledge-point evidence from stored facts. It is
// deliberately read-only: the mastery table is a cache written when a self
// declaration changes, never a prerequisite for answering this query.
func (s *Service) buildMastery(ctx context.Context, q querier, userID string) (map[string]*kpEvidence, error) {
	result := map[string]*kpEvidence{}
	get := func(kpID string) *kpEvidence {
		if result[kpID] == nil {
			result[kpID] = newKPEvidence()
		}
		return result[kpID]
	}

	// Attempt facts joined to their knowledge points.
	attemptRows, err := q.Query(ctx, `
		select qkp.knowledge_point_id, a.id, a.correct, a.assisted, a.answered_at
		from released_attempts a
		join question_knowledge_points qkp
		  on qkp.question_id = a.question_id and qkp.status <> 'rejected'
		where a.user_id = $1::uuid`, userID)
	if err != nil {
		return nil, fmt.Errorf("load mastery attempts: %w", err)
	}
	for attemptRows.Next() {
		var (
			kpID     string
			attempt  string
			correct  bool
			assisted bool
			answered time.Time
		)
		if err := attemptRows.Scan(&kpID, &attempt, &correct, &assisted, &answered); err != nil {
			attemptRows.Close()
			return nil, fmt.Errorf("scan mastery attempt: %w", err)
		}
		get(kpID).add(learning.EvidenceKindOf(correct, assisted), "attempt:"+attempt, answered)
	}
	attemptRows.Close()
	if err := attemptRows.Err(); err != nil {
		return nil, err
	}

	// Explicit evidence rows. Self-declarations are read newest first so a
	// revocation (mastered=false) can switch the state back off.
	evidenceRows, err := q.Query(ctx, `
		select e.knowledge_point_id, e.kind, e.id::text, coalesce(e.material_id, ''), e.payload, e.occurred_at
		from evidence e
		where e.user_id = $1::uuid and e.knowledge_point_id is not null
		order by e.occurred_at desc, e.created_at desc`, userID)
	if err != nil {
		return nil, fmt.Errorf("load mastery evidence: %w", err)
	}
	selfDeclaredSeen := map[string]bool{}
	for evidenceRows.Next() {
		var (
			kpID     string
			kind     string
			id       string
			material string
			payload  json.RawMessage
			occurred time.Time
		)
		if err := evidenceRows.Scan(&kpID, &kind, &id, &material, &payload, &occurred); err != nil {
			evidenceRows.Close()
			return nil, fmt.Errorf("scan mastery evidence: %w", err)
		}
		agg := get(kpID)
		evidenceKind := learning.EvidenceKind(kind)
		if evidenceKind == learning.KindSelfDeclared {
			agg.count(evidenceKind)
			if !selfDeclaredSeen[kpID] {
				selfDeclaredSeen[kpID] = true
				var declared selfDeclaredPayload
				_ = json.Unmarshal(payload, &declared)
				if declared.Mastered {
					agg.mark(learning.KindSelfDeclared, "self_declared:"+kpID)
				}
			}
			continue
		}
		agg.add(evidenceKind, evidenceDedupeKey(evidenceFact{ID: id, Kind: evidenceKind, MaterialID: material}), occurred)
	}
	evidenceRows.Close()
	if err := evidenceRows.Err(); err != nil {
		return nil, err
	}

	// A finished reading of a material linked from a question counts as
	// reading evidence for that question's knowledge points.
	progressRows, err := q.Query(ctx, `
		select qkp.knowledge_point_id, rp.material_id, rp.updated_at
		from reading_progress rp
		join knowledge_links kl on kl.material_id = rp.material_id and kl.status <> 'rejected'
		join question_knowledge_points qkp on qkp.question_id = kl.question_id and qkp.status <> 'rejected'
		where rp.user_id = $1::uuid and rp.done`, userID)
	if err != nil {
		return nil, fmt.Errorf("load mastery reading: %w", err)
	}
	for progressRows.Next() {
		var (
			kpID     string
			material string
			updated  time.Time
		)
		if err := progressRows.Scan(&kpID, &material, &updated); err != nil {
			progressRows.Close()
			return nil, fmt.Errorf("scan mastery reading: %w", err)
		}
		agg := get(kpID)
		key := "material:" + material
		if !agg.hasKindKey(learning.KindReading, key) {
			agg.add(learning.KindReading, key, updated)
		}
	}
	progressRows.Close()
	return result, progressRows.Err()
}

// recordedMastery is the persisted state used for transparency in the mastery
// list. It never overrides the freshly derived state.
type recordedMastery struct {
	State         string
	EvidenceCount int
	UpdatedAt     time.Time
}

func loadRecordedMastery(ctx context.Context, q querier, userID string) (map[string]recordedMastery, error) {
	rows, err := q.Query(ctx, `
		select knowledge_point_id, state, evidence_count, updated_at
		from mastery where user_id = $1::uuid`, userID)
	if err != nil {
		return nil, fmt.Errorf("load recorded mastery: %w", err)
	}
	defer rows.Close()
	recorded := map[string]recordedMastery{}
	for rows.Next() {
		var (
			kpID string
			item recordedMastery
		)
		if err := rows.Scan(&kpID, &item.State, &item.EvidenceCount, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan recorded mastery: %w", err)
		}
		recorded[kpID] = item
	}
	return recorded, rows.Err()
}

type masteryItem struct {
	KnowledgePointID string                `json:"knowledgePointId"`
	Name             string                `json:"name"`
	Module           string                `json:"module"`
	State            learning.MasteryState `json:"state"`
	RecordedState    string                `json:"recordedState"`
	EvidenceCount    int                   `json:"evidenceCount"`
	Counts           map[string]int        `json:"counts"`
	LastEvidenceAt   *time.Time            `json:"lastEvidenceAt,omitempty"`
	RuleVersion      string                `json:"ruleVersion"`
}

// ---------------------------------------------------------------- review plan

type reviewCandidate struct {
	AttemptID    string
	QuestionID   string
	Revision     string
	SessionID    string
	Correct      bool
	Assisted     bool
	AnsweredAt   time.Time
	DueAt        time.Time
	IntervalDays int
}

// deriveReviewPlan returns the due review candidates derived only from attempt
// facts. Nothing is written: review_items stays owned by practice, and this
// query cannot change history.
func (s *Service) deriveReviewPlan(ctx context.Context, q querier, userID string, now time.Time) ([]reviewCandidate, error) {
	rows, err := q.Query(ctx, `
		select distinct on (a.question_id)
		       a.id, a.question_id, a.question_revision, coalesce(a.session_id::text, ''),
		       a.correct, a.assisted, a.answered_at
		from released_attempts a
		where a.user_id = $1::uuid
		order by a.question_id, a.answered_at desc, a.created_at desc, a.id desc`, userID)
	if err != nil {
		return nil, fmt.Errorf("load review attempts: %w", err)
	}
	defer rows.Close()
	items := []reviewCandidate{}
	for rows.Next() {
		var item reviewCandidate
		if err := rows.Scan(&item.AttemptID, &item.QuestionID, &item.Revision, &item.SessionID,
			&item.Correct, &item.Assisted, &item.AnsweredAt); err != nil {
			return nil, fmt.Errorf("scan review attempt: %w", err)
		}
		item.IntervalDays = learning.ReviewIntervalDays(item.Correct, item.Assisted)
		item.DueAt = learning.ReviewDueAt(item.AnsweredAt, item.Correct, item.Assisted)
		if item.DueAt.After(now) {
			continue
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].DueAt.Equal(items[j].DueAt) {
			return items[i].QuestionID < items[j].QuestionID
		}
		return items[i].DueAt.Before(items[j].DueAt)
	})
	return items, nil
}

func reviewReasonLabel(kind learning.EvidenceKind, intervalDays int) string {
	switch kind {
	case learning.KindWrong:
		return fmt.Sprintf("最近一次作答错误，按 %d 天间隔复习", intervalDays)
	case learning.KindAssistedPass:
		return fmt.Sprintf("最近一次作答使用了提示辅助，按 %d 天间隔复习", intervalDays)
	default:
		return fmt.Sprintf("最近一次为独立通过，按 %d 天间隔复习", intervalDays)
	}
}

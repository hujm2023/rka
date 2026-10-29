package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"rka/internal/auth"
	"rka/internal/httpx"
	"rka/internal/learning"
	"rka/internal/store"
)

// ---------------------------------------------------------------- helpers

func parseLimit(r *http.Request, fallback, max int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, httpx.NewError(http.StatusBadRequest, "bad_request", "分页参数 limit 必须是正整数。")
	}
	if value > max {
		value = max
	}
	return value, nil
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

// ---------------------------------------------------------------- summary

func (s *Service) handleSummary(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	limit, err := parseLimit(r, 20, 100)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	data, err := s.buildSummary(r.Context(), user.ID, limit)
	if err != nil {
		logRequestError(s.log, r, "learning summary", err)
		httpx.Error(w, r, err)
		return
	}
	now, err := s.db.Now(r.Context())
	if err != nil {
		logRequestError(s.log, r, "learning summary time", err)
		httpx.Error(w, r, err)
		return
	}
	// The flat aliases keep older dashboard clients working while counts stays
	// the canonical evidence-kind map.
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"generatedAt":     now,
		"counts":          data.Counts,
		"reading":         data.Counts[string(learning.KindReading)],
		"assisted":        data.Counts[string(learning.KindAssistedPass)],
		"independent":     data.Counts[string(learning.KindIndependentPass)],
		"selfDeclared":    data.Counts[string(learning.KindSelfDeclared)],
		"recent":          data.Recent,
		"readingProgress": data.ReadingProgress,
	})
}

// ---------------------------------------------------------------- reading

type readingRequest struct {
	MaterialID        string  `json:"materialId"`
	MaterialVersionID string  `json:"materialVersionId"`
	Progress          float64 `json:"progress"`
	SectionID         string  `json:"sectionId"`
	SectionKey        string  `json:"sectionKey"`
	Offset            int     `json:"offset"`
	Done              *bool   `json:"done"`
}

func sameReading(a, b readingPayload) bool {
	return math.Abs(a.Progress-b.Progress) < 1e-9 &&
		a.SectionAnchor == b.SectionAnchor &&
		a.SectionKey == b.SectionKey &&
		a.Offset == b.Offset &&
		a.Done == b.Done &&
		a.MaterialVersionID == b.MaterialVersionID
}

func (s *Service) handleReading(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	var req readingRequest
	if err := httpx.Decode(w, r, &req, 32<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	req.MaterialID = strings.TrimSpace(req.MaterialID)
	if req.MaterialID == "" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "missing_material", "缺少资料标识。"))
		return
	}
	if req.Progress < 0 || req.Progress > 1 {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_progress", "阅读进度必须在 0 到 1 之间。"))
		return
	}
	if req.Offset < 0 {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_offset", "阅读位置不能为负数。"))
		return
	}

	var reviewStatus, currentVersion string
	err := s.db.Pool.QueryRow(r.Context(), `
		select review_status, coalesce(current_version_id::text, '')
		from materials where id = $1`, req.MaterialID).Scan(&reviewStatus, &currentVersion)
	if err != nil {
		if errors.Is(store.MapError(err), store.ErrNotFound) {
			httpx.Error(w, r, httpx.NewError(http.StatusNotFound, "material_not_found", "资料不存在。"))
			return
		}
		s.log.Error("reading material", "error", err)
		httpx.Error(w, r, err)
		return
	}
	if !s.auth.CanAccessMaterial(r.Context(), user, req.MaterialID) {
		httpx.Error(w, r, httpx.NewError(http.StatusForbidden, "material_forbidden", "没有访问该资料的权限。"))
		return
	}

	versionID := strings.TrimSpace(req.MaterialVersionID)
	if versionID != "" {
		var exists bool
		if err := s.db.Pool.QueryRow(r.Context(), `
			select exists (
				select 1 from material_versions where id = $1::uuid and material_id = $2
			)`, versionID, req.MaterialID).Scan(&exists); err != nil || !exists {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "unknown_version", "资料版本不存在或不属于该资料。"))
			return
		}
	} else {
		versionID = currentVersion
		if versionID == "" {
			err := s.db.Pool.QueryRow(r.Context(), `
				select id::text from material_versions
				where material_id = $1 order by version desc limit 1`, req.MaterialID).Scan(&versionID)
			if err != nil {
				if errors.Is(store.MapError(err), store.ErrNotFound) {
					httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "material_version_required", "该资料还没有可用版本。"))
					return
				}
				s.log.Error("reading version", "error", err)
				httpx.Error(w, r, err)
				return
			}
		}
	}

	// Section anchors are advisory: a learner may still be reading a cached
	// copy of a previous revision. An unknown anchor is reported explicitly in
	// the response instead of rejecting the whole progress report.
	anchor := strings.TrimSpace(req.SectionID)
	sectionKey := strings.TrimSpace(req.SectionKey)
	sectionResolved := false
	switch {
	case anchor != "":
		var exists bool
		if err := s.db.Pool.QueryRow(r.Context(), `
			select exists (
				select 1 from material_sections where material_version_id = $1::uuid and anchor = $2
			)`, versionID, anchor).Scan(&exists); err != nil {
			s.log.Error("reading section", "error", err)
			httpx.Error(w, r, err)
			return
		}
		sectionResolved = exists
	case sectionKey != "":
		if err := s.db.Pool.QueryRow(r.Context(), `
			select anchor from material_sections
			where material_version_id = $1::uuid and stable_key = $2
			order by start_offset limit 1`, versionID, sectionKey).Scan(&anchor); err != nil {
			if !errors.Is(store.MapError(err), store.ErrNotFound) {
				s.log.Error("reading section key", "error", err)
				httpx.Error(w, r, err)
				return
			}
		} else {
			sectionResolved = true
		}
	}

	payload := readingPayload{
		MaterialVersionID: versionID,
		Progress:          req.Progress,
		SectionAnchor:     anchor,
		SectionKey:        sectionKey,
		Offset:            req.Offset,
	}

	var (
		evidenceID   string
		occurredAt   time.Time
		deduplicated bool
	)
	err = s.db.Tx(r.Context(), func(tx pgx.Tx) error {
		// Serialize first writes too; row locking cannot protect an absent row.
		if _, err := tx.Exec(r.Context(), `select pg_advisory_xact_lock(hashtextextended($1, 0))`, "reading:"+user.ID+":"+req.MaterialID); err != nil {
			return err
		}
		if req.Done != nil {
			payload.Done = *req.Done
		} else {
			err := tx.QueryRow(r.Context(), `select done from reading_progress where user_id=$1::uuid and material_id=$2`, user.ID, req.MaterialID).Scan(&payload.Done)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `
			insert into reading_progress
				(user_id, material_id, material_version_id, progress, section_anchor, section_offset, done, updated_at)
			values ($1::uuid, $2, $3::uuid, $4, $5, $6, $7, now())
			on conflict (user_id, material_id) do update set
				material_version_id = excluded.material_version_id,
				progress = excluded.progress,
				section_anchor = excluded.section_anchor,
				section_offset = excluded.section_offset,
				done = excluded.done,
				updated_at = now()`,
			user.ID, req.MaterialID, versionID, req.Progress, anchor, req.Offset, payload.Done); err != nil {
			return err
		}
		// Repeating the exact same report must not inflate the evidence count;
		// any actual state change still appends a new fact.
		var previousID string
		var previousPayload []byte
		var previousAt time.Time
		err = tx.QueryRow(r.Context(), `
			select id::text, payload, occurred_at from evidence
			where user_id = $1::uuid and kind = 'reading' and material_id = $2
			order by occurred_at desc, created_at desc limit 1`, user.ID, req.MaterialID).
			Scan(&previousID, &previousPayload, &previousAt)
		if err == nil {
			var previous readingPayload
			if json.Unmarshal(previousPayload, &previous) == nil && sameReading(previous, payload) {
				evidenceID, occurredAt, deduplicated = previousID, previousAt, true
				return nil
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return tx.QueryRow(r.Context(), `
			insert into evidence (user_id, kind, material_id, material_version_id, rule_version, payload, occurred_at)
			values ($1::uuid, 'reading', $2, $3::uuid, $4, $5, now())
			returning id::text, occurred_at`, user.ID, req.MaterialID, versionID, learning.RuleVersion, encoded).
			Scan(&evidenceID, &occurredAt)
	})
	if err != nil {
		s.log.Error("reading progress", "error", err)
		httpx.Error(w, r, err)
		return
	}

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"reading": map[string]any{
			"materialId":        req.MaterialID,
			"materialVersionId": versionID,
			"progress":          req.Progress,
			"sectionAnchor":     anchor,
			"sectionKey":        sectionKey,
			"sectionOffset":     req.Offset,
			"sectionResolved":   sectionResolved,
			"done":              payload.Done,
		},
		"evidence": evidenceRef{
			ID: evidenceID, Kind: learning.KindReading, Source: "evidence", OccurredAt: occurredAt,
			MaterialID: req.MaterialID, MaterialVersionID: versionID, SectionAnchor: anchor, SectionKey: sectionKey,
		},
		"deduplicated": deduplicated,
	})
}

// ---------------------------------------------------------------- self declared

type selfDeclaredRequest struct {
	KnowledgePointID string `json:"knowledgePointId"`
	Mastered         *bool  `json:"mastered"`
}

func (s *Service) handleSelfDeclared(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	var req selfDeclaredRequest
	if err := httpx.Decode(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	req.KnowledgePointID = strings.TrimSpace(req.KnowledgePointID)
	if req.KnowledgePointID == "" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "missing_knowledge_point", "缺少知识点标识。"))
		return
	}
	if req.Mastered == nil {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "missing_mastered", "请说明是声明掌握还是撤销声明。"))
		return
	}
	var name string
	if err := s.db.Pool.QueryRow(r.Context(), `
		select name from knowledge_points where id = $1`, req.KnowledgePointID).Scan(&name); err != nil {
		if errors.Is(store.MapError(err), store.ErrNotFound) {
			httpx.Error(w, r, httpx.NewError(http.StatusNotFound, "knowledge_point_not_found", "知识点不存在。"))
			return
		}
		s.log.Error("self declared knowledge point", "error", err)
		httpx.Error(w, r, err)
		return
	}

	payload, err := json.Marshal(selfDeclaredPayload{Mastered: *req.Mastered})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var (
		evidenceID   string
		occurredAt   time.Time
		state        learning.MasteryState
		counts       map[string]int
		evidenceCnt  int
		deduplicated bool
	)
	err = s.db.Tx(r.Context(), func(tx pgx.Tx) error {
		// Repeating the same declaration is idempotent; an actual state change
		// (declare -> revoke -> declare) still appends a new evidence row.
		var previousID string
		var previousPayload []byte
		var previousAt time.Time
		err := tx.QueryRow(r.Context(), `
			select id::text, payload, occurred_at from evidence
			where user_id = $1::uuid and kind = 'self_declared' and knowledge_point_id = $2
			order by occurred_at desc, created_at desc limit 1`, user.ID, req.KnowledgePointID).
			Scan(&previousID, &previousPayload, &previousAt)
		if err == nil {
			var previous selfDeclaredPayload
			if json.Unmarshal(previousPayload, &previous) == nil && previous.Mastered == *req.Mastered {
				evidenceID, occurredAt, deduplicated = previousID, previousAt, true
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if evidenceID == "" {
			if err := tx.QueryRow(r.Context(), `
				insert into evidence (user_id, kind, knowledge_point_id, rule_version, payload, occurred_at)
				values ($1::uuid, 'self_declared', $2, $3, $4, now())
				returning id::text, occurred_at`, user.ID, req.KnowledgePointID, learning.RuleVersion, payload).
				Scan(&evidenceID, &occurredAt); err != nil {
				return err
			}
		}
		all, err := s.buildMastery(r.Context(), tx, user.ID)
		if err != nil {
			return err
		}
		agg := all[req.KnowledgePointID]
		if agg == nil {
			agg = newKPEvidence()
		}
		state = agg.state()
		counts = agg.counts
		evidenceCnt = agg.evidenceCount()
		_, err = tx.Exec(r.Context(), `
			insert into mastery (user_id, knowledge_point_id, state, evidence_count, rule_version, updated_at)
			values ($1::uuid, $2, $3, $4, $5, now())
			on conflict (user_id, knowledge_point_id) do update set
				state = excluded.state,
				evidence_count = excluded.evidence_count,
				rule_version = excluded.rule_version,
				updated_at = now()`,
			user.ID, req.KnowledgePointID, string(state), evidenceCnt, learning.RuleVersion)
		return err
	})
	if err != nil {
		s.log.Error("self declaration", "error", err)
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"knowledgePointId": req.KnowledgePointID,
		"name":             name,
		"mastered":         *req.Mastered,
		"state":            state,
		"counts":           counts,
		"evidenceCount":    evidenceCnt,
		"evidence":         evidenceRef{ID: evidenceID, Kind: learning.KindSelfDeclared, Source: "evidence", OccurredAt: occurredAt, KnowledgePointID: req.KnowledgePointID, Mastered: req.Mastered},
		"deduplicated":     deduplicated,
		"ruleVersion":      learning.RuleVersion,
	})
}

// ---------------------------------------------------------------- mastery

func (s *Service) handleMastery(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	limit, err := parseLimit(r, 100, 500)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	includeUnknown := r.URL.Query().Get("includeUnknown") == "1" || r.URL.Query().Get("includeUnknown") == "true"
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))

	aggs, err := s.buildMastery(r.Context(), s.db.Pool, user.ID)
	if err != nil {
		s.log.Error("mastery derivation", "error", err)
		httpx.Error(w, r, err)
		return
	}
	recorded, err := loadRecordedMastery(r.Context(), s.db.Pool, user.ID)
	if err != nil {
		s.log.Error("mastery recorded", "error", err)
		httpx.Error(w, r, err)
		return
	}

	type pointInfo struct {
		Name   string
		Module string
	}
	info := map[string]pointInfo{}
	ids := []string{}
	if includeUnknown {
		rows, err := s.db.Pool.Query(r.Context(), `select id, name, module from knowledge_points order by id`)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		for rows.Next() {
			var id string
			var point pointInfo
			if err := rows.Scan(&id, &point.Name, &point.Module); err != nil {
				rows.Close()
				httpx.Error(w, r, err)
				return
			}
			info[id] = point
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			httpx.Error(w, r, err)
			return
		}
	} else {
		for id, agg := range aggs {
			if agg.evidenceCount() == 0 && recorded[id].State == "" {
				continue
			}
			ids = append(ids, id)
		}
		if len(ids) > 0 {
			rows, err := s.db.Pool.Query(r.Context(), `
				select id, name, module from knowledge_points where id = any($1::text[])`, ids)
			if err != nil {
				httpx.Error(w, r, err)
				return
			}
			for rows.Next() {
				var id string
				var point pointInfo
				if err := rows.Scan(&id, &point.Name, &point.Module); err != nil {
					rows.Close()
					httpx.Error(w, r, err)
					return
				}
				info[id] = point
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				httpx.Error(w, r, err)
				return
			}
		}
	}

	sort.Strings(ids)
	start := 0
	if cursor != "" {
		for start < len(ids) && ids[start] <= cursor {
			start++
		}
	}
	end := start + limit
	if end > len(ids) {
		end = len(ids)
	}
	items := make([]masteryItem, 0, end-start)
	for _, id := range ids[start:end] {
		point, ok := info[id]
		if !ok {
			continue
		}
		agg := aggs[id]
		if agg == nil {
			agg = newKPEvidence()
		}
		rec := recorded[id]
		items = append(items, masteryItem{
			KnowledgePointID: id,
			Name:             point.Name,
			Module:           point.Module,
			State:            agg.state(),
			RecordedState:    rec.State,
			EvidenceCount:    agg.evidenceCount(),
			Counts:           agg.counts,
			LastEvidenceAt:   optionalTime(agg.last),
			RuleVersion:      learning.RuleVersion,
		})
	}
	nextCursor := ""
	if end < len(ids) && len(items) > 0 {
		nextCursor = items[len(items)-1].KnowledgePointID
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"items":       items,
		"nextCursor":  nextCursor,
		"ruleVersion": learning.RuleVersion,
	})
}

// ---------------------------------------------------------------- review plan

type reviewReason struct {
	Code           string    `json:"code"`
	Label          string    `json:"label"`
	IntervalDays   int       `json:"intervalDays"`
	LastAnsweredAt time.Time `json:"lastAnsweredAt"`
	Correct        bool      `json:"correct"`
	Assisted       bool      `json:"assisted"`
	OverdueDays    int       `json:"overdueDays"`
}

type reviewPlanItem struct {
	QuestionID       string       `json:"questionId"`
	QuestionRevision string       `json:"questionRevision"`
	AttemptID        string       `json:"attemptId"`
	SessionID        string       `json:"sessionId,omitempty"`
	DueAt            time.Time    `json:"dueAt"`
	IntervalDays     int          `json:"intervalDays"`
	Reason           reviewReason `json:"reason"`
	ReasonText       string       `json:"reasonText"`
}

func (s *Service) handleReviewPlan(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	limit, err := parseLimit(r, 50, 200)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	offset := 0
	if cursor := strings.TrimSpace(r.URL.Query().Get("cursor")); cursor != "" {
		offset, err = strconv.Atoi(cursor)
		if err != nil || offset < 0 {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_request", "分页游标不正确。"))
			return
		}
	}
	now, err := s.db.Now(r.Context())
	if err != nil {
		s.log.Error("review plan time", "error", err)
		httpx.Error(w, r, err)
		return
	}
	candidates, err := s.deriveReviewPlan(r.Context(), s.db.Pool, user.ID, now)
	if err != nil {
		s.log.Error("review plan", "error", err)
		httpx.Error(w, r, err)
		return
	}
	total := len(candidates)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	items := make([]reviewPlanItem, 0, end-offset)
	for _, item := range candidates[offset:end] {
		kind := learning.EvidenceKindOf(item.Correct, item.Assisted)
		overdue := int(now.Sub(item.DueAt).Hours() / 24)
		if overdue < 0 {
			overdue = 0
		}
		label := reviewReasonLabel(kind, item.IntervalDays)
		items = append(items, reviewPlanItem{
			QuestionID:       item.QuestionID,
			QuestionRevision: item.Revision,
			AttemptID:        item.AttemptID,
			SessionID:        item.SessionID,
			DueAt:            item.DueAt,
			IntervalDays:     item.IntervalDays,
			ReasonText:       label,
			Reason: reviewReason{
				Code:           string(kind),
				Label:          label,
				IntervalDays:   item.IntervalDays,
				LastAnsweredAt: item.AnsweredAt,
				Correct:        item.Correct,
				Assisted:       item.Assisted,
				OverdueDays:    overdue,
			},
		})
	}
	nextCursor := ""
	if end < total {
		nextCursor = strconv.Itoa(end)
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"generatedAt": now,
		"items":       items,
		"nextCursor":  nextCursor,
		"dueCount":    total,
	})
}

// ---------------------------------------------------------------- report

type reportCounts struct {
	Wrong           int `json:"wrong"`
	AssistedPass    int `json:"assisted_pass"`
	IndependentPass int `json:"independent_pass"`
	Due             int `json:"due"`
}

type reportBasis struct {
	Statement   string   `json:"statement,omitempty"`
	Kind        string   `json:"kind"`
	Count       int      `json:"count"`
	Source      string   `json:"source"`
	EvidenceIDs []string `json:"evidenceIds"`
	QuestionIDs []string `json:"questionIds,omitempty"`
}

func (s *Service) handleReport(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	now, err := s.db.Now(r.Context())
	if err != nil {
		s.log.Error("report time", "error", err)
		httpx.Error(w, r, err)
		return
	}
	attempts, err := loadAttempts(r.Context(), s.db.Pool, user.ID)
	if err != nil {
		s.log.Error("report attempts", "error", err)
		httpx.Error(w, r, err)
		return
	}
	dueItems, err := s.deriveReviewPlan(r.Context(), s.db.Pool, user.ID, now)
	if err != nil {
		s.log.Error("report due", "error", err)
		httpx.Error(w, r, err)
		return
	}

	counts := reportCounts{Due: len(dueItems)}
	wrongIDs := []string{}
	assistedIDs := []string{}
	independentIDs := []string{}
	refByID := make(map[string]evidenceRef, len(attempts))
	for _, fact := range attempts {
		refByID[fact.ID] = attemptRef(fact)
		kind := learning.EvidenceKindOf(fact.Correct, fact.Assisted)
		switch kind {
		case learning.KindWrong:
			counts.Wrong++
			if len(wrongIDs) < 10 {
				wrongIDs = append(wrongIDs, fact.ID)
			}
		case learning.KindAssistedPass:
			counts.AssistedPass++
			if len(assistedIDs) < 10 {
				assistedIDs = append(assistedIDs, fact.ID)
			}
		case learning.KindIndependentPass:
			counts.IndependentPass++
			if len(independentIDs) < 10 {
				independentIDs = append(independentIDs, fact.ID)
			}
		}
	}
	dueAttemptIDs := []string{}
	dueQuestionIDs := []string{}
	for _, item := range dueItems {
		if len(dueAttemptIDs) >= 10 {
			break
		}
		dueAttemptIDs = append(dueAttemptIDs, item.AttemptID)
		dueQuestionIDs = append(dueQuestionIDs, item.QuestionID)
	}

	conclusion := learning.BuildReport(counts.IndependentPass, counts.AssistedPass, counts.Wrong, counts.Due)

	// Build one traceable basis group per statement BuildReport emits. The
	// pure rule appends statements in this fixed order, so counts and evidence
	// ids line up one-to-one.
	type basisGroup struct {
		kind        string
		count       int
		evidenceIDs []string
		questionIDs []string
	}
	groups := make([]basisGroup, 0, 4)
	if counts.Wrong > 0 {
		groups = append(groups, basisGroup{kind: "wrong", count: counts.Wrong, evidenceIDs: wrongIDs})
	}
	if counts.AssistedPass > 0 {
		groups = append(groups, basisGroup{kind: "assisted_pass", count: counts.AssistedPass, evidenceIDs: assistedIDs})
	}
	if counts.Due > 0 {
		groups = append(groups, basisGroup{kind: "due_review", count: counts.Due, evidenceIDs: dueAttemptIDs, questionIDs: dueQuestionIDs})
	}
	if counts.IndependentPass > 0 {
		groups = append(groups, basisGroup{kind: "independent_pass", count: counts.IndependentPass, evidenceIDs: independentIDs})
	}
	basis := make([]reportBasis, 0, len(groups))
	for index, group := range groups {
		statement := ""
		if index < len(conclusion.Evidence) {
			statement = conclusion.Evidence[index]
		}
		basis = append(basis, reportBasis{
			Statement:   statement,
			Kind:        group.kind,
			Count:       group.count,
			Source:      "attempt",
			EvidenceIDs: group.evidenceIDs,
			QuestionIDs: group.questionIDs,
		})
	}

	// Evidence list: recent attempts plus any attempt referenced by a basis
	// statement, plus recent reading/self-declaration evidence.
	refs := []evidenceRef{}
	seen := map[string]bool{}
	addRef := func(ref evidenceRef) {
		if ref.ID == "" || seen[ref.ID] {
			return
		}
		seen[ref.ID] = true
		refs = append(refs, ref)
	}
	for _, fact := range attempts {
		if len(refs) >= 100 {
			break
		}
		addRef(refByID[fact.ID])
	}
	for _, group := range groups {
		for _, id := range group.evidenceIDs {
			if ref, ok := refByID[id]; ok {
				addRef(ref)
			}
		}
	}
	learningEvidence, err := loadEvidence(r.Context(), s.db.Pool, user.ID, []learning.EvidenceKind{
		learning.KindReading, learning.KindSelfDeclared,
	})
	if err != nil {
		s.log.Error("report evidence", "error", err)
		httpx.Error(w, r, err)
		return
	}
	for index, fact := range learningEvidence {
		if index >= 50 {
			break
		}
		addRef(evidenceRefOf(fact))
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].OccurredAt.Equal(refs[j].OccurredAt) {
			return refs[i].ID > refs[j].ID
		}
		return refs[i].OccurredAt.After(refs[j].OccurredAt)
	})

	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"generatedAt": now,
		"counts":      counts,
		"conclusion":  conclusion,
		"basis":       basis,
		"evidence":    refs,
		"calibration": map[string]any{
			"calibrated": false,
			"note":       "平台不提供概率性评分；报告结论只依据上方可回查的学习事实。",
		},
	})
}

// logRequestError keeps client disconnects (navigating away cancels the
// request context) at debug level; only real failures are errors.
func logRequestError(log *slog.Logger, r *http.Request, message string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		log.Debug(message+" (client disconnected)", "error", err, "request_id", httpx.RequestIDFrom(r.Context()))
		return
	}
	log.Error(message, "error", err, "request_id", httpx.RequestIDFrom(r.Context()))
}

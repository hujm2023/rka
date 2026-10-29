package content

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"rka/internal/auth"
	"rka/internal/httpx"
)

// searchItem is one hit of the combined material / section / question search.
// Section hits always carry a materialId + anchor pair so the reader can jump
// to the exact source position.
type searchItem struct {
	Type       string   `json:"type"`
	MaterialID string   `json:"materialId,omitempty"`
	Anchor     string   `json:"anchor,omitempty"`
	SectionID  string   `json:"sectionId,omitempty"`
	SectionKey string   `json:"sectionKey,omitempty"`
	Title      string   `json:"title"`
	Path       []string `json:"path,omitempty"`
	Snippet    string   `json:"snippet"`
	QuestionID string   `json:"questionId,omitempty"`
	Revision   string   `json:"revision,omitempty"`
	Module     string   `json:"module,omitempty"`
}

// likeAll builds one AND group per whitespace-separated term, matching any of
// the given columns. Chinese text is not word-segmented by PostgreSQL's
// tsvector, so explicit ILIKE groups are both simpler and more predictable.
// Placeholders are $1..$len(terms), matching termsToArgs.
func likeAll(terms []string, columns ...string) string {
	parts := make([]string, 0, len(terms))
	for index := range terms {
		placeholder := "$" + strconv.Itoa(index+1)
		or := make([]string, 0, len(columns))
		for _, column := range columns {
			or = append(or, column+" ilike "+placeholder)
		}
		parts = append(parts, "("+strings.Join(or, " or ")+")")
	}
	return strings.Join(parts, " and ")
}

func termsToArgs(terms []string) []any {
	args := make([]any, 0, len(terms))
	for _, term := range terms {
		args = append(args, "%"+escapeLike(term)+"%")
	}
	return args
}

// escapeLike keeps user supplied wildcards literal. The backslash is LIKE's
// default escape character, so it has to be escaped before the wildcards it
// would otherwise escape.
func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

// handleSearch searches material titles, section titles/bodies and question
// stems in one round trip. Results are ordered materials → sections →
// questions, and each hit is actionable: materialId + anchor for reading,
// questionId + revision for practice.
func (s *Service) handleSearch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := auth.MustUser(ctx)
	raw := strings.TrimSpace(r.URL.Query().Get("q"))
	if raw == "" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "missing_query", "请输入检索关键词。"))
		return
	}
	limit := intQuery(r.URL.Query().Get("limit"), 20, 1, 50)
	terms := strings.Fields(raw)
	if len(terms) == 0 {
		terms = []string{raw}
	}
	// Reserve room for every result family: section hits must not crowd out
	// question hits or the reverse.
	materialCap := 5
	questionCap := limit / 3
	if questionCap < 3 {
		questionCap = 3
	}
	if questionCap > 10 {
		questionCap = 10
	}

	materials := []searchItem{}
	sectionHits := []searchItem{}
	questionHits := []searchItem{}

	// 1. Material title / book matches.
	materialWhere := likeAll(terms, "m.title", "m.book")
	materialArgs := termsToArgs(terms)
	if !user.IsAdmin() {
		materialArgs = append(materialArgs, user.ID)
		materialWhere += fmt.Sprintf(` and (
			m.review_status = 'published'
			or exists (
				select 1 from content_grants g
				where g.user_id = $%d::uuid and g.revoked_at is null
				  and g.resource_type = 'material' and g.resource_id = m.id))`, len(materialArgs))
	}
	materialArgs = append(materialArgs, materialCap)
	rows, err := s.db.Pool.Query(ctx, `
		select m.id, m.title,
		       coalesce((select s.anchor from material_sections s
		                 where s.material_version_id = v.id order by s.start_offset limit 1), '')
		from materials m
		join material_versions v on v.id = m.current_version_id
		where `+materialWhere+`
		order by m.id limit $`+strconv.Itoa(len(materialArgs)), materialArgs...)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	for rows.Next() {
		var materialID, title, anchor string
		if err := rows.Scan(&materialID, &title, &anchor); err != nil {
			rows.Close()
			httpx.Error(w, r, err)
			return
		}
		materials = append(materials, searchItem{
			Type: "material", MaterialID: materialID, Anchor: anchor, Title: title, Snippet: title,
		})
	}
	rows.Close()

	// 2. Section title / body matches inside the current published versions.
	sectionWhere := likeAll(terms, "s.title", "s.body")
	sectionArgs := termsToArgs(terms)
	if !user.IsAdmin() {
		sectionArgs = append(sectionArgs, user.ID)
		sectionWhere += fmt.Sprintf(` and (
			m.review_status = 'published'
			or exists (
				select 1 from content_grants g
				where g.user_id = $%d::uuid and g.revoked_at is null
				  and g.resource_type = 'material' and g.resource_id = m.id))`, len(sectionArgs))
	}
	sectionArgs = append(sectionArgs, limit)
	rows, err = s.db.Pool.Query(ctx, `
		select m.id, s.anchor, s.stable_key, s.title, s.path, s.body, s.start_offset
		from material_sections s
		join material_versions v on v.id = s.material_version_id
		join materials m on m.id = v.material_id
		where v.id = m.current_version_id and `+sectionWhere+`
		order by m.id, s.start_offset limit $`+strconv.Itoa(len(sectionArgs)), sectionArgs...)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	for rows.Next() {
		var materialID, anchor, stableKey, title, body string
		var startOffset int
		var path []string
		if err := rows.Scan(&materialID, &anchor, &stableKey, &title, &path, &body, &startOffset); err != nil {
			rows.Close()
			httpx.Error(w, r, err)
			return
		}
		if path == nil {
			path = []string{}
		}
		sectionHits = append(sectionHits, searchItem{
			Type: "section", MaterialID: materialID, Anchor: anchor, SectionID: anchor,
			SectionKey: stableKey, Title: title, Path: path, Snippet: snippetAround(body, terms[0], 60),
		})
	}
	rows.Close()

	// 3. Question stem / knowledge / term matches.
	questionWhere := likeAll(terms, "v.stem", "q.knowledge", "q.term")
	questionArgs := termsToArgs(terms)
	questionArgs = append(questionArgs, questionCap)
	rows, err = s.db.Pool.Query(ctx, `
		select q.id, coalesce(v.revision, ''), coalesce(v.stem, ''), q.module
		from questions q
		join question_versions v on v.id = q.current_version_id
		where `+questionWhere+`
		order by q.id limit $`+strconv.Itoa(len(questionArgs)), questionArgs...)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	for rows.Next() {
		var questionID, revision, stem, module string
		if err := rows.Scan(&questionID, &revision, &stem, &module); err != nil {
			rows.Close()
			httpx.Error(w, r, err)
			return
		}
		questionHits = append(questionHits, searchItem{
			Type: "question", QuestionID: questionID, Revision: revision, Module: module,
			Title: stem, Snippet: snippetAround(stem, terms[0], 20),
		})
	}
	rows.Close()

	if len(materials) > limit {
		materials = materials[:limit]
	}
	items := make([]searchItem, 0, limit)
	items = append(items, materials...)
	remaining := limit - len(items)
	if remaining < 0 {
		remaining = 0
	}
	questionTake := len(questionHits)
	if questionTake > remaining {
		questionTake = remaining
	}
	sectionTake := len(sectionHits)
	if sectionTake > remaining-questionTake {
		sectionTake = remaining - questionTake
	}
	items = append(items, sectionHits[:sectionTake]...)
	items = append(items, questionHits[:questionTake]...)
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"query":      raw,
		"items":      items,
		"nextCursor": "",
	})
}

// snippetAround returns a short window around the first case-insensitive match
// of needle in text, ellipsized on both sides.
func snippetAround(text, needle string, radius int) string {
	lowerText := strings.ToLower(text)
	lowerNeedle := strings.ToLower(needle)
	index := strings.Index(lowerText, lowerNeedle)
	if index < 0 {
		return firstRunes(text, radius*2)
	}
	start := index - radius*3
	if start < 0 {
		start = 0
	}
	end := index + len(needle) + radius*3
	if end > len(text) {
		end = len(text)
	}
	// Keep UTF-8 boundaries intact.
	for start > 0 && !utf8Start(text[start]) {
		start--
	}
	for end < len(text) && !utf8Start(text[end]) {
		end++
	}
	snippet := strings.TrimSpace(text[start:end])
	snippet = strings.Join(strings.Fields(snippet), " ")
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(text) {
		snippet += "…"
	}
	return snippet
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func firstRunes(text string, count int) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) <= count {
		return string(runes)
	}
	return string(runes[:count]) + "…"
}

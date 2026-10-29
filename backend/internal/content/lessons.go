package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"rka/internal/auth"
	"rka/internal/httpx"
)

// lessonView is one legacy lesson index entry. The catalog returns metadata
// only; the body is served by GET /api/content/lessons/{id}.
type lessonView struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Book       string `json:"book"`
	Module     string `json:"module"`
	SourceFile string `json:"sourceFile"`
	SourceURL  string `json:"sourceUrl"`
	License    string `json:"license"`
	Chars      int    `json:"chars"`
	Minutes    int    `json:"minutes"`
	Excerpt    string `json:"excerpt"`
	MaterialID string `json:"materialId,omitempty"`
	SectionKey string `json:"sectionKey,omitempty"`
}

// normalizeLessonTitle builds the comparison key used to map a legacy lesson
// onto a material section. Headings and lesson titles differ only by inline
// markdown and leading emoji ("⭐必答题高频" vs "必答题高频"), so those are
// dropped before comparison; everything else must match exactly.
func normalizeLessonTitle(title string) string {
	var builder strings.Builder
	builder.Grow(len(title))
	for _, r := range title {
		if unicode.IsSpace(r) {
			continue
		}
		switch r {
		case '*', '`', '#', '>', '_', '~':
			continue
		}
		if isEmojiRune(r) {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

// isEmojiRune reports the pictographic ranges that appear in snapshot headings.
func isEmojiRune(r rune) bool {
	switch {
	case r >= 0x1F300 && r <= 0x1FAFF: // pictographs, symbols, transport
		return true
	case r >= 0x2600 && r <= 0x27BF: // misc symbols, dingbats
		return true
	case r >= 0x2B00 && r <= 0x2BFF: // stars and arrows (⭐, ⬆)
		return true
	case r >= 0x2190 && r <= 0x21FF: // arrows (←, →)
		return true
	case r == 0xFE0F || r == 0xFE0E: // variation selectors
		return true
	}
	return false
}

// ---------------------------------------------------------------- import

// importLessons indexes the legacy lesson entries and resolves each one to the
// material and section it was cut from. Resolution is best effort: an
// unresolved entry keeps empty links instead of guessing.
func (s *Service) importLessons(ctx context.Context, tx pgx.Tx, snapshotDir string, lessons []snapshotLesson, state *importState) error {
	if len(lessons) == 0 {
		return nil
	}
	materialBySource, err := loadMaterialSourceIndex(ctx, tx)
	if err != nil {
		return err
	}
	sectionIndex, err := loadSectionTitleIndex(ctx, tx)
	if err != nil {
		return err
	}
	for _, lesson := range lessons {
		err := withSavepoint(ctx, tx, func(nested pgx.Tx) error {
			return s.importLesson(ctx, nested, snapshotDir, lesson, materialBySource, sectionIndex, state)
		})
		if err != nil {
			state.fail("lesson:"+lesson.ID, "%s", err.Error())
			continue
		}
		state.add("lessons", 1)
	}
	return nil
}

func (s *Service) importLesson(ctx context.Context, tx pgx.Tx, snapshotDir string, lesson snapshotLesson, materialBySource map[string]string, sectionIndex map[string]map[string]string, state *importState) error {
	if !safeIDPattern.MatchString(lesson.ID) {
		return fmt.Errorf("lesson ID 不安全或为空")
	}
	data, err := os.ReadFile(lessonPath(snapshotDir, lesson.ID))
	if err != nil {
		return fmt.Errorf("读取正文失败：%v", err)
	}
	if len(data) == 0 {
		return fmt.Errorf("正文为空")
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	relPath, err := s.saveOriginal("lessons", sha, data)
	if err != nil {
		return fmt.Errorf("保存正文失败：%v", err)
	}
	if _, err := tx.Exec(ctx, `
		insert into blobs (sha256, size, media_type, rel_path)
		values ($1, $2, 'text/markdown; charset=utf-8', $3)
		on conflict (sha256) do nothing`, sha, len(data), relPath); err != nil {
		return fmt.Errorf("写入正文记录失败：%v", err)
	}

	materialID := materialBySource[lesson.SourceFile]
	sectionKey := ""
	if materialID != "" {
		state.add("lessonsWithMaterial", 1)
		sectionKey = sectionIndex[materialID][normalizeLessonTitle(lesson.Title)]
		if sectionKey != "" {
			state.add("lessonsWithSection", 1)
		}
	}
	chars := lesson.Chars
	if chars <= 0 {
		chars = utf8.RuneCountInString(string(data))
	}
	minutes := lesson.Minutes
	if minutes <= 0 {
		minutes = minutesFor(chars)
	}
	var material any
	if materialID != "" {
		material = materialID
	}
	var section any
	if sectionKey != "" {
		section = sectionKey
	}
	if _, err := tx.Exec(ctx, `
		insert into content_lessons
			(id, title, book, module, source_file, source_url, license, chars, minutes,
			 excerpt, body_sha256, material_id, section_stable_key)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		on conflict (id) do update set
			title = excluded.title, book = excluded.book, module = excluded.module,
			source_file = excluded.source_file, source_url = excluded.source_url,
			license = excluded.license, chars = excluded.chars, minutes = excluded.minutes,
			excerpt = excluded.excerpt, body_sha256 = excluded.body_sha256,
			material_id = excluded.material_id, section_stable_key = excluded.section_stable_key`,
		lesson.ID, cleanText(lesson.Title), cleanText(lesson.Book), cleanText(lesson.Module),
		cleanText(lesson.SourceFile), cleanText(lesson.SourceURL), cleanText(lesson.License),
		chars, minutes, cleanText(lesson.Excerpt), sha, material, section); err != nil {
		return fmt.Errorf("写入 lesson 失败：%v", err)
	}
	return nil
}

// loadMaterialSourceIndex maps a snapshot source_file to the imported material.
func loadMaterialSourceIndex(ctx context.Context, tx pgx.Tx) (map[string]string, error) {
	rows, err := tx.Query(ctx, `select id, source_file from materials order by id`)
	if err != nil {
		return nil, fmt.Errorf("query material sources: %w", err)
	}
	defer rows.Close()
	index := map[string]string{}
	for rows.Next() {
		var id, sourceFile string
		if err := rows.Scan(&id, &sourceFile); err != nil {
			return nil, err
		}
		if sourceFile == "" {
			continue
		}
		if _, exists := index[sourceFile]; !exists {
			index[sourceFile] = id
		}
	}
	return index, rows.Err()
}

// loadSectionTitleIndex maps each material's current sections by normalized
// title. Duplicate titles keep the first section in reading order, matching the
// old reader's `sections.find(...)` behaviour.
func loadSectionTitleIndex(ctx context.Context, tx pgx.Tx) (map[string]map[string]string, error) {
	rows, err := tx.Query(ctx, `
		select m.id, s.title, s.stable_key
		from materials m
		join material_versions v on v.id = m.current_version_id
		join material_sections s on s.material_version_id = v.id
		order by m.id, s.start_offset`)
	if err != nil {
		return nil, fmt.Errorf("query section titles: %w", err)
	}
	defer rows.Close()
	index := map[string]map[string]string{}
	for rows.Next() {
		var materialID, title, stableKey string
		if err := rows.Scan(&materialID, &title, &stableKey); err != nil {
			return nil, err
		}
		key := normalizeLessonTitle(title)
		if key == "" {
			continue
		}
		if index[materialID] == nil {
			index[materialID] = map[string]string{}
		}
		if _, exists := index[materialID][key]; !exists {
			index[materialID][key] = stableKey
		}
	}
	return index, rows.Err()
}

// ---------------------------------------------------------------- reading

// loadLessons returns the legacy lesson index without bodies, ordered by book
// and id so the response is stable across imports. Legacy entries inherit the
// visibility of the material they were cut from: a withdrawn or explicitly
// granted material must not leak its lesson metadata through the catalog.
func (s *Service) loadLessons(ctx context.Context, user auth.User) ([]lessonView, error) {
	var grantUser any
	if !user.IsAdmin() {
		grantUser = user.ID
	}
	rows, err := s.db.Pool.Query(ctx, `
		select l.id, l.title, l.book, l.module, l.source_file, l.source_url, l.license,
		       l.chars, l.minutes, l.excerpt, coalesce(l.material_id, ''),
		       coalesce(l.section_stable_key, '')
		from content_lessons l
		left join materials m on m.id = l.material_id
		where l.material_id is null
		   or $1
		   or m.review_status = 'published'
		   or exists (
		       select 1 from content_grants g
		       where g.user_id = $2::uuid and g.revoked_at is null
		         and g.resource_type = 'material' and g.resource_id = l.material_id)
		order by l.book, l.id`, user.IsAdmin(), grantUser)
	if err != nil {
		return nil, fmt.Errorf("query lessons: %w", err)
	}
	defer rows.Close()
	lessons := []lessonView{}
	for rows.Next() {
		var lesson lessonView
		if err := rows.Scan(&lesson.ID, &lesson.Title, &lesson.Book, &lesson.Module,
			&lesson.SourceFile, &lesson.SourceURL, &lesson.License, &lesson.Chars,
			&lesson.Minutes, &lesson.Excerpt, &lesson.MaterialID, &lesson.SectionKey); err != nil {
			return nil, err
		}
		lessons = append(lessons, lesson)
	}
	return lessons, rows.Err()
}

// handleLesson returns one legacy lesson with its markdown body read from the
// immutable original. The body is not rewritten or summarised. When the entry
// was resolved to a material, the same material-access rule as the reader
// endpoints applies, so a withdrawn or restricted material cannot leak through
// the legacy deep link.
func (s *Service) handleLesson(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	var (
		lesson lessonView
		sha    string
		rel    string
	)
	err := s.db.Pool.QueryRow(ctx, `
		select id, title, book, module, source_file, source_url, license, chars, minutes,
		       excerpt, coalesce(material_id, ''), coalesce(section_stable_key, ''), body_sha256
		from content_lessons where id = $1`, id).
		Scan(&lesson.ID, &lesson.Title, &lesson.Book, &lesson.Module, &lesson.SourceFile,
			&lesson.SourceURL, &lesson.License, &lesson.Chars, &lesson.Minutes, &lesson.Excerpt,
			&lesson.MaterialID, &lesson.SectionKey, &sha)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpx.Error(w, r, httpx.ErrNotFound)
			return
		}
		httpx.Error(w, r, err)
		return
	}
	if lesson.MaterialID != "" && !s.auth.CanAccessMaterial(ctx, auth.MustUser(ctx), lesson.MaterialID) {
		httpx.Error(w, r, httpx.ErrForbidden)
		return
	}
	if err := s.db.Pool.QueryRow(ctx, `select rel_path from blobs where sha256 = $1`, sha).Scan(&rel); err != nil {
		httpx.Error(w, r, httpx.NewError(http.StatusInternalServerError, "lesson_body_missing", "该条目的正文不可用。"))
		return
	}
	path, err := s.originalPath(rel)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	body, err := os.ReadFile(path)
	if err != nil {
		s.log.Error("read lesson original", "lesson", id, "path", path, "error", err)
		httpx.Error(w, r, httpx.NewError(http.StatusInternalServerError, "lesson_body_missing", "该条目的正文不可用。"))
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"lesson": map[string]any{
			"id": lesson.ID, "title": lesson.Title, "book": lesson.Book, "module": lesson.Module,
			"sourceFile": lesson.SourceFile, "sourceUrl": lesson.SourceURL, "license": lesson.License,
			"chars": lesson.Chars, "minutes": lesson.Minutes, "excerpt": lesson.Excerpt,
			"materialId": lesson.MaterialID, "sectionKey": lesson.SectionKey,
			"body": string(body),
		},
	})
}

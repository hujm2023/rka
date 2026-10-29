package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"rka/internal/store"
)

// jobError is one non-fatal item failure surfaced on the import job. Failures
// are never silently skipped: an unreadable material, an invalid question or a
// link pointing at a missing section all end up here.
type jobError struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

type importState struct {
	counts map[string]int
	errs   []jobError
}

const maxJobErrors = 200

// withSavepoint runs one item's database work in a savepoint so a bad item
// cannot abort the whole import transaction; the failure is recorded on the
// job instead.
func withSavepoint(ctx context.Context, tx pgx.Tx, fn func(pgx.Tx) error) error {
	nested, err := tx.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(nested); err != nil {
		_ = nested.Rollback(context.WithoutCancel(ctx))
		return err
	}
	return nested.Commit(ctx)
}

func newImportState() *importState {
	return &importState{counts: map[string]int{}}
}

func (st *importState) add(key string, n int) { st.counts[key] += n }

func (st *importState) fail(target, format string, args ...any) {
	if len(st.errs) >= maxJobErrors {
		return
	}
	st.errs = append(st.errs, jobError{Target: target, Reason: fmt.Sprintf(format, args...)})
}

func (st *importState) errorStrings() []string {
	if len(st.errs) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(st.errs))
	for _, item := range st.errs {
		out = append(out, item.Target+": "+item.Reason)
	}
	return out
}

// materialArtifact is the parse product of one material version, written under
// cfg.DerivedDir() after the database transaction commits.
type materialArtifact struct {
	MaterialID string    `json:"materialId"`
	SHA256     string    `json:"sha256"`
	Version    int       `json:"version"`
	Chars      int       `json:"chars"`
	Minutes    int       `json:"minutes"`
	Title      string    `json:"title"`
	Sections   []Section `json:"sections"`
}

type derivedSection struct {
	Anchor    string   `json:"anchor"`
	StableKey string   `json:"stableKey"`
	Title     string   `json:"title"`
	Level     int      `json:"level"`
	Start     int      `json:"start"`
	Path      []string `json:"path"`
	Body      string   `json:"body"`
}

var safeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// cleanText removes NUL bytes, which PostgreSQL text and jsonb reject.
func cleanText(value string) string {
	return strings.ReplaceAll(value, "\x00", "")
}

// pgJSON prepares a JSON document for a jsonb parameter: PostgreSQL rejects
// the \u0000 escape sequence that some source documents contain.
func pgJSON(raw []byte) string {
	return strings.ReplaceAll(string(raw), `\u0000`, "")
}

// validInternalID accepts database only identifiers (question and subjective
// item ids contain Chinese terms) while still bounding length.
func validInternalID(id string) bool {
	return id != "" && len(id) <= 200 && !strings.ContainsAny(id, "\x00\n\r")
}

// ImportSnapshot imports the bundled content snapshot idempotently.
//
// The import is content addressed and item idempotent: re-importing the same
// catalog version creates no new rows and never overwrites user data
// (review statuses, annotation targets, attempts). A new material version is
// inserted as unpublished, then published inside the same transaction, so
// readers keep seeing the previous version until the new one is complete.
func (s *Service) ImportSnapshot(ctx context.Context, snapshotDir string, actorID string) (ImportResult, error) {
	jobID, err := s.createImportJob(ctx, actorID)
	if err != nil {
		return ImportResult{}, fmt.Errorf("create import job: %w", err)
	}
	result, itemErrors, err := s.runImport(ctx, snapshotDir, jobID)
	if err != nil {
		s.finishImportJob(ctx, jobID, result, itemErrors, err)
		return result, err
	}
	s.finishImportJob(ctx, jobID, result, itemErrors, nil)
	return result, nil
}

func (s *Service) runImport(ctx context.Context, snapshotDir, jobID string) (ImportResult, []jobError, error) {
	result := ImportResult{JobID: jobID}
	catalog, err := loadSnapshot(snapshotDir)
	if err != nil {
		return result, nil, err
	}
	if len(catalog.Materials) == 0 || len(catalog.Choices) == 0 {
		return result, nil, fmt.Errorf("content snapshot is empty: %d materials, %d questions",
			len(catalog.Materials), len(catalog.Choices))
	}
	result.Version = catalog.Version
	state := newImportState()

	if done, err := s.snapshotAlreadyImported(ctx, catalog); err != nil {
		return result, nil, err
	} else if done {
		result.Skipped = true
		result.Published = true
		result.Counts = s.contentCounts(ctx)
		return result, nil, nil
	}

	var artifacts []materialArtifact
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		// Serialise concurrent imports (web request, CLI init, worker) so two
		// runs cannot allocate the same material version number.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1)`, importLockKey); err != nil {
			return fmt.Errorf("acquire import lock: %w", err)
		}
		if err := s.importMaterials(ctx, tx, snapshotDir, catalog.Materials, state, &artifacts); err != nil {
			return err
		}
		if err := s.importLessons(ctx, tx, snapshotDir, catalog.Lessons, state); err != nil {
			return err
		}
		if err := s.importQuestions(ctx, tx, catalog.Choices, state); err != nil {
			return err
		}
		if err := s.importKnowledgeLinks(ctx, tx, catalog.KnowledgeLinks, state); err != nil {
			return err
		}
		if err := s.importSubjectiveItems(ctx, tx, catalog.Cases, catalog.Essays, state); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			update platform_state
			set content_version = $1, bank_version = $1, updated_at = now()
			where id = 1`, catalog.Version); err != nil {
			return fmt.Errorf("record content version: %w", err)
		}
		return nil
	})
	if err != nil {
		result.Errors = state.errorStrings()
		return result, state.errs, err
	}

	// Derived parse products live outside the transaction: a failure to write
	// them is reported on the job but the authoritative database rows remain.
	s.writeDerived(catalog.Version, artifacts, state)

	result.Counts = state.counts
	result.Errors = state.errorStrings()
	result.Published = true
	return result, state.errs, nil
}

// snapshotAlreadyImported reports whether the exact same catalog is already
// fully imported, letting repeated `rka init` runs skip the heavy work.
func (s *Service) snapshotAlreadyImported(ctx context.Context, catalog *snapshotCatalog) (bool, error) {
	var version string
	if err := s.db.Pool.QueryRow(ctx, `select content_version from platform_state where id = 1`).Scan(&version); err != nil {
		return false, fmt.Errorf("read content version: %w", err)
	}
	if version != catalog.Version {
		return false, nil
	}
	// Only a clean previous run of this version counts as already imported; a
	// partial run must be retried so its failures are not frozen in.
	var stats []byte
	err := s.db.Pool.QueryRow(ctx, `
		select stats from jobs
		where kind = 'content_import' and status = 'completed'
		order by created_at desc limit 1`).Scan(&stats)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("read last import job: %w", err)
	}
	var parsed struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(stats, &parsed)
	if parsed.Version != catalog.Version {
		return false, nil
	}
	var materials, questions, subjective, lessons int
	if err := s.db.Pool.QueryRow(ctx, `
		select (select count(*) from materials),
		       (select count(*) from questions),
		       (select count(*) from subjective_items),
		       (select count(*) from content_lessons)`).Scan(&materials, &questions, &subjective, &lessons); err != nil {
		return false, fmt.Errorf("count imported content: %w", err)
	}
	return materials == len(catalog.Materials) &&
		questions == len(catalog.Choices) &&
		subjective == len(catalog.Cases)+len(catalog.Essays) &&
		lessons == len(catalog.Lessons), nil
}

func (s *Service) contentCounts(ctx context.Context) map[string]int {
	counts := map[string]int{}
	queries := map[string]string{
		"materials":               `select count(*) from materials`,
		"materialVersions":        `select count(*) from material_versions`,
		"sections":                `select count(*) from material_sections`,
		"questions":               `select count(*) from questions`,
		"questionVersions":        `select count(*) from question_versions`,
		"knowledgePoints":         `select count(*) from knowledge_points`,
		"questionKnowledgePoints": `select count(*) from question_knowledge_points`,
		"knowledgeLinks":          `select count(*) from knowledge_links`,
		"subjectiveItems":         `select count(*) from subjective_items`,
		"lessons":                 `select count(*) from content_lessons`,
		"blobs":                   `select count(*) from blobs`,
	}
	for key, query := range queries {
		var value int
		if err := s.db.Pool.QueryRow(ctx, query).Scan(&value); err == nil {
			counts[key] = value
		}
	}
	return counts
}

func (s *Service) importMaterials(ctx context.Context, tx pgx.Tx, snapshotDir string, materials []snapshotMaterial, state *importState, artifacts *[]materialArtifact) error {
	for _, material := range materials {
		if !safeIDPattern.MatchString(material.ID) {
			state.fail("material:"+material.ID, "资料 ID 不安全或为空，已跳过")
			continue
		}
		var artifact *materialArtifact
		err := withSavepoint(ctx, tx, func(nested pgx.Tx) error {
			var innerErr error
			artifact, innerErr = s.importMaterial(ctx, nested, snapshotDir, material)
			return innerErr
		})
		if err != nil {
			state.fail("material:"+material.ID, "%s", err.Error())
			continue
		}
		state.add("materials", 1)
		state.add("materialVersions", 1)
		state.add("sections", len(artifact.Sections))
		*artifacts = append(*artifacts, *artifact)
	}
	return nil
}

func (s *Service) importMaterial(ctx context.Context, tx pgx.Tx, snapshotDir string, material snapshotMaterial) (*materialArtifact, error) {
	switch material.Kind {
	case "textbook", "outline", "notes", "guide":
	default:
		return nil, fmt.Errorf("未知资料类型 %q", material.Kind)
	}
	data, err := os.ReadFile(materialPath(snapshotDir, material.ID))
	if err != nil {
		return nil, fmt.Errorf("读取原件失败：%v", err)
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if material.SourceHash != "" && material.SourceHash != sha {
		return nil, fmt.Errorf("原件哈希与目录不一致：目录 %s，实际 %s", material.SourceHash, sha)
	}
	relPath, err := s.saveOriginal("materials", sha, data)
	if err != nil {
		return nil, fmt.Errorf("保存原件失败：%v", err)
	}
	if _, err := tx.Exec(ctx, `
		insert into blobs (sha256, size, media_type, rel_path)
		values ($1, $2, 'text/markdown; charset=utf-8', $3)
		on conflict (sha256) do nothing`, sha, len(data), relPath); err != nil {
		return nil, fmt.Errorf("写入原件记录失败：%v", err)
	}
	if _, err := tx.Exec(ctx, `
		insert into materials (id, title, book, kind, module, source_file, source_url, license)
		values ($1, $2, $3, $4, $5, $6, $7, $8)
		on conflict (id) do update set
			title = excluded.title, book = excluded.book, kind = excluded.kind,
			module = excluded.module, source_file = excluded.source_file,
			source_url = excluded.source_url, license = excluded.license,
			updated_at = now()`,
		material.ID, cleanText(material.Title), cleanText(material.Book), material.Kind, cleanText(material.Module),
		cleanText(material.SourceFile), cleanText(material.SourceURL), cleanText(material.License)); err != nil {
		return nil, fmt.Errorf("写入资料失败：%v", err)
	}

	text := string(data)
	sections := ParseSections(text)
	chars := utf8.RuneCountInString(text)
	minutes := minutesFor(chars)
	excerpt := material.Excerpt
	if excerpt == "" {
		excerpt = excerptFor(sections, 80)
	}

	var versionID string
	var version int
	var published bool
	err = tx.QueryRow(ctx, `
		select id::text, version, published_at is not null
		from material_versions where material_id = $1 and sha256 = $2`,
		material.ID, sha).Scan(&versionID, &version, &published)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if err := tx.QueryRow(ctx, `
			insert into material_versions (material_id, version, sha256, chars, minutes, excerpt, index_status)
			values ($1, (select coalesce(max(version), 0) + 1 from material_versions where material_id = $1),
			        $2, $3, $4, $5, 'building')
			returning id::text, version`,
			material.ID, sha, chars, minutes, excerpt).Scan(&versionID, &version); err != nil {
			return nil, fmt.Errorf("写入资料版本失败：%v", err)
		}
		if err := insertSections(ctx, tx, versionID, sections); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
			update material_versions set index_status = 'ready', published_at = now(), index_error = ''
			where id = $1::uuid`, versionID); err != nil {
			return nil, fmt.Errorf("发布资料版本失败：%v", err)
		}
	case err != nil:
		return nil, fmt.Errorf("查询资料版本失败：%v", err)
	default:
		// Same bytes imported before: keep the row, only fill missing parsed
		// text (an interrupted previous run) and publish if still pending.
		if err := insertSections(ctx, tx, versionID, sections); err != nil {
			return nil, err
		}
		if !published {
			if _, err := tx.Exec(ctx, `
				update material_versions set index_status = 'ready', published_at = now(), index_error = ''
				where id = $1::uuid`, versionID); err != nil {
				return nil, fmt.Errorf("发布资料版本失败：%v", err)
			}
		}
	}

	// Only move the current pointer forward in time: an admin-curated
	// replacement version created later must not be clobbered by an old
	// snapshot re-import.
	if _, err := tx.Exec(ctx, `
		update materials m set current_version_id = v.id, updated_at = now()
		from material_versions v
		where m.id = $1 and v.id = $2::uuid
		  and (m.current_version_id is null
		       or (select created_at from material_versions where id = m.current_version_id) <= v.created_at)`,
		material.ID, versionID); err != nil {
		return nil, fmt.Errorf("更新资料当前版本失败：%v", err)
	}

	return &materialArtifact{
		MaterialID: material.ID,
		SHA256:     sha,
		Version:    version,
		Chars:      chars,
		Minutes:    minutes,
		Title:      material.Title,
		Sections:   sections,
	}, nil
}

func insertSections(ctx context.Context, tx pgx.Tx, versionID string, sections []Section) error {
	for _, section := range sections {
		path := section.Path
		if path == nil {
			path = []string{}
		}
		if _, err := tx.Exec(ctx, `
			insert into material_sections
				(material_version_id, anchor, stable_key, title, level, start_offset, path, body)
			values ($1::uuid, $2, $3, $4, $5, $6, $7, $8)
			on conflict (material_version_id, anchor) do update set
				body = case when material_sections.body = '' then excluded.body else material_sections.body end,
				title = case when material_sections.title = '' then excluded.title else material_sections.title end`,
			versionID, section.Anchor, section.StableKey, section.Title, section.Level, section.Start, path, section.Body); err != nil {
			return fmt.Errorf("写入小节 %s 失败：%v", section.Anchor, err)
		}
	}
	return nil
}

// saveOriginal stores an immutable original under data/originals/<kind> and
// returns its path relative to the originals root. Existing files are never
// overwritten: the name is the content hash, so equal names mean equal bytes.
func (s *Service) saveOriginal(kind, sha string, data []byte) (string, error) {
	dir := filepath.Join(s.cfg.OriginalsDir(), kind)
	dest := filepath.Join(dir, sha+".md")
	if info, err := os.Stat(dest); err == nil {
		if info.Size() != int64(len(data)) {
			return "", fmt.Errorf("内容寻址文件 %s 已存在但大小不符", dest)
		}
		return kind + "/" + sha + ".md", nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".import-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o640); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return "", err
	}
	return kind + "/" + sha + ".md", nil
}

func (s *Service) importQuestions(ctx context.Context, tx pgx.Tx, questions []snapshotQuestion, state *importState) error {
	for _, question := range questions {
		err := withSavepoint(ctx, tx, func(nested pgx.Tx) error {
			return s.importQuestion(ctx, nested, question)
		})
		if err != nil {
			state.fail("question:"+question.ID, "%s", err.Error())
			continue
		}
		state.add("questions", 1)
		state.add("questionVersions", 1)
		if strings.TrimSpace(question.Knowledge) != "" {
			state.add("knowledgePoints", 1)
			state.add("questionKnowledgePoints", 1)
		}
	}
	return nil
}

func (s *Service) importQuestion(ctx context.Context, tx pgx.Tx, question snapshotQuestion) error {
	if !validInternalID(question.ID) {
		return fmt.Errorf("题目 ID 为空或过长")
	}
	switch question.SourceType {
	case "real", "mock", "ai":
	default:
		return fmt.Errorf("未知题目来源 %q", question.SourceType)
	}
	if len(question.Options) == 0 {
		return fmt.Errorf("题目缺少选项")
	}
	if _, ok := question.Options[question.Answer]; !ok {
		return fmt.Errorf("答案 %q 不在选项内", question.Answer)
	}
	revision := strings.TrimSpace(question.Revision)
	if revision == "" {
		revision = revisionForQuestion(question.Stem, question.Options, question.Answer, question.Analysis)
	}
	options, err := json.Marshal(question.Options)
	if err != nil {
		return fmt.Errorf("序列化选项失败：%v", err)
	}
	optionsJSON := pgJSON(options)
	if _, err := tx.Exec(ctx, `
		insert into questions
			(id, source_type, term, paper, question_no, module, knowledge, difficulty, figure_missing, multi_blank)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		on conflict (id) do update set
			source_type = excluded.source_type, term = excluded.term, paper = excluded.paper,
			question_no = excluded.question_no, module = excluded.module, knowledge = excluded.knowledge,
			difficulty = excluded.difficulty, figure_missing = excluded.figure_missing,
			multi_blank = excluded.multi_blank, updated_at = now()`,
		question.ID, question.SourceType, cleanText(question.Term), cleanText(question.Paper), question.QuestionNo,
		cleanText(question.Module), cleanText(question.Knowledge), cleanText(question.Difficulty),
		question.FigureMissing, question.MultiBlank); err != nil {
		return fmt.Errorf("写入题目失败：%v", err)
	}
	var figure any
	if len(question.Figure) > 0 && string(question.Figure) != "null" {
		figure = pgJSON(question.Figure)
	}
	if _, err := tx.Exec(ctx, `
		insert into question_versions
			(question_id, revision, stem, options, answer, analysis, scoring_rule,
			 source_file, source_label, source_url, figure)
		values ($1, $2, $3, $4::jsonb, $5, $6, $7::jsonb, $8, $9, $10, $11::jsonb)
		on conflict (question_id, revision) do nothing`,
		question.ID, revision, cleanText(question.Stem), optionsJSON, cleanText(question.Answer), cleanText(question.Analysis),
		defaultScoringRule, cleanText(question.SourceFile), cleanText(question.SourceLabel), cleanText(question.SourceURL), figure); err != nil {
		return fmt.Errorf("写入题目版本失败：%v", err)
	}
	if _, err := tx.Exec(ctx, `
		update questions q set current_version_id = v.id, updated_at = now()
		from question_versions v
		where q.id = $1 and v.question_id = $1 and v.revision = $2
		  and (q.current_version_id is null
		       or (select created_at from question_versions where id = q.current_version_id) <= v.created_at)`,
		question.ID, revision); err != nil {
		return fmt.Errorf("更新题目当前版本失败：%v", err)
	}
	if strings.TrimSpace(question.Knowledge) != "" {
		pointID := knowledgePointID(question.Module, question.Knowledge)
		if _, err := tx.Exec(ctx, `
			insert into knowledge_points (id, name, module) values ($1, $2, $3)
			on conflict (id) do nothing`, pointID, question.Knowledge, question.Module); err != nil {
			return fmt.Errorf("写入知识点失败：%v", err)
		}
		if _, err := tx.Exec(ctx, `
			insert into question_knowledge_points (question_id, knowledge_point_id, source, confidence, status)
			values ($1, $2, 'auto', 0.9, 'auto')
			on conflict (question_id, knowledge_point_id) do nothing`, question.ID, pointID); err != nil {
			return fmt.Errorf("写入题目知识点关联失败：%v", err)
		}
	}
	return nil
}

const defaultScoringRule = `{"type":"single-choice","points":1,"ruleVersion":"v1"}`

func revisionForQuestion(stem string, options map[string]string, answer, analysis string) string {
	payload, _ := json.Marshal(map[string]any{
		"stem":     stem,
		"options":  options,
		"answer":   answer,
		"analysis": analysis,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])[:16]
}

func knowledgePointID(module, name string) string {
	sum := sha256.Sum256([]byte(module + "\x1f" + strings.TrimSpace(name)))
	return "kp-" + hex.EncodeToString(sum[:])[:12]
}

type sectionRef struct {
	ID        string
	Anchor    string
	StableKey string
}

func (s *Service) importKnowledgeLinks(ctx context.Context, tx pgx.Tx, links map[string][]snapshotLink, state *importState) error {
	if len(links) == 0 {
		return nil
	}
	index, err := loadSectionIndex(ctx, tx)
	if err != nil {
		return err
	}
	questionIDs := make([]string, 0, len(links))
	for questionID := range links {
		questionIDs = append(questionIDs, questionID)
	}
	sort.Strings(questionIDs)
	for _, questionID := range questionIDs {
		for _, link := range links[questionID] {
			target := fmt.Sprintf("link:%s->%s:%s", questionID, link.MaterialID, link.SectionID)
			ref, ok := index.resolve(link.MaterialID, link.SectionID)
			if !ok {
				state.fail(target, "原文小节不存在")
				continue
			}
			terms := link.Terms
			if terms == nil {
				terms = []string{}
			}
			err := withSavepoint(ctx, tx, func(nested pgx.Tx) error {
				_, execErr := nested.Exec(ctx, `
					insert into knowledge_links
						(question_id, material_id, material_version_id, section_id, section_stable_key, terms, score, status)
					values ($1, $2, (select current_version_id from materials where id = $2), $3::uuid, $4, $5, $6, 'auto')
					on conflict (question_id, section_id) do nothing`,
					questionID, link.MaterialID, ref.ID, ref.StableKey, terms, link.Score)
				return execErr
			})
			if err != nil {
				state.fail(target, "写入关联失败：%v", err)
				continue
			}
			state.add("knowledgeLinks", 1)
		}
	}
	return nil
}

type sectionIndex struct {
	current    map[string]map[string]sectionRef // material -> anchor -> current section
	currentKey map[string]map[string]sectionRef // material -> stable key -> current section
	historical map[string]map[string]string     // material -> old anchor -> stable key
}

// resolve maps a snapshot anchor to the current version. When a new material
// version moved the headings, the old anchor no longer exists; the stable key
// recorded for that anchor keeps the link meaningful across versions.
func (index sectionIndex) resolve(materialID, anchor string) (sectionRef, bool) {
	if byAnchor, ok := index.current[materialID]; ok {
		if ref, ok := byAnchor[anchor]; ok {
			return ref, true
		}
	}
	if key, ok := index.historical[materialID][anchor]; ok {
		if ref, ok := index.currentKey[materialID][key]; ok {
			return ref, true
		}
	}
	return sectionRef{}, false
}

func loadSectionIndex(ctx context.Context, tx pgx.Tx) (sectionIndex, error) {
	index := sectionIndex{
		current:    map[string]map[string]sectionRef{},
		currentKey: map[string]map[string]sectionRef{},
		historical: map[string]map[string]string{},
	}
	rows, err := tx.Query(ctx, `
		select m.id, s.anchor, s.id::text, s.stable_key
		from materials m
		join material_versions v on v.id = m.current_version_id
		join material_sections s on s.material_version_id = v.id`)
	if err != nil {
		return index, fmt.Errorf("query current sections: %w", err)
	}
	for rows.Next() {
		var materialID string
		var ref sectionRef
		if err := rows.Scan(&materialID, &ref.Anchor, &ref.ID, &ref.StableKey); err != nil {
			rows.Close()
			return index, err
		}
		if index.current[materialID] == nil {
			index.current[materialID] = map[string]sectionRef{}
			index.currentKey[materialID] = map[string]sectionRef{}
		}
		index.current[materialID][ref.Anchor] = ref
		index.currentKey[materialID][ref.StableKey] = ref
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return index, err
	}
	rows, err = tx.Query(ctx, `
		select v.material_id, s.anchor, s.stable_key
		from material_sections s
		join material_versions v on v.id = s.material_version_id`)
	if err != nil {
		return index, fmt.Errorf("query section history: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var materialID, anchor, key string
		if err := rows.Scan(&materialID, &anchor, &key); err != nil {
			return index, err
		}
		if index.historical[materialID] == nil {
			index.historical[materialID] = map[string]string{}
		}
		index.historical[materialID][anchor] = key
	}
	return index, rows.Err()
}

func (s *Service) importSubjectiveItems(ctx context.Context, tx pgx.Tx, cases, essays []json.RawMessage, state *importState) error {
	items := make([]struct {
		kind string
		raw  json.RawMessage
	}, 0, len(cases)+len(essays))
	for _, raw := range cases {
		items = append(items, struct {
			kind string
			raw  json.RawMessage
		}{"case", raw})
	}
	for _, raw := range essays {
		items = append(items, struct {
			kind string
			raw  json.RawMessage
		}{"essay", raw})
	}
	for _, item := range items {
		var meta subjectiveMeta
		if err := json.Unmarshal(item.raw, &meta); err != nil {
			state.fail("subjective:unknown", "解析失败：%v", err)
			continue
		}
		if !validInternalID(meta.ID) || meta.Title == "" {
			state.fail("subjective:"+meta.ID, "缺少 ID 或标题")
			continue
		}
		switch meta.SourceType {
		case "real", "mock", "ai":
		default:
			meta.SourceType = "real"
		}
		revision := meta.Revision
		if revision == "" {
			sum := sha256.Sum256(item.raw)
			revision = hex.EncodeToString(sum[:])[:16]
		}
		err := withSavepoint(ctx, tx, func(nested pgx.Tx) error {
			_, execErr := nested.Exec(ctx, `
				insert into subjective_items
					(id, kind, revision, title, module, term, paper, source_type, source_url, payload)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)
				on conflict (id) do update set
					kind = excluded.kind, revision = excluded.revision, title = excluded.title,
					module = excluded.module, term = excluded.term, paper = excluded.paper,
					source_type = excluded.source_type, source_url = excluded.source_url,
					payload = excluded.payload, updated_at = now()`,
				meta.ID, item.kind, revision, cleanText(meta.Title), cleanText(meta.Module),
				cleanText(meta.Term), cleanText(meta.Paper), meta.SourceType, cleanText(meta.SourceURL),
				pgJSON(item.raw))
			return execErr
		})
		if err != nil {
			state.fail("subjective:"+meta.ID, "写入失败：%v", err)
			continue
		}
		state.add("subjectiveItems", 1)
	}
	return nil
}

// writeDerived persists the parsed section tree and a version manifest under
// data/derived so the index can be inspected and rebuilt without the database.
func (s *Service) writeDerived(version string, artifacts []materialArtifact, state *importState) {
	root := filepath.Join(s.cfg.DerivedDir(), "materials")
	for _, artifact := range artifacts {
		dir := filepath.Join(root, artifact.MaterialID)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			state.fail("derived:"+artifact.MaterialID, "创建解析目录失败：%v", err)
			continue
		}
		payload := struct {
			MaterialID string           `json:"materialId"`
			SHA256     string           `json:"sha256"`
			Version    int              `json:"version"`
			Title      string           `json:"title"`
			Chars      int              `json:"chars"`
			Minutes    int              `json:"minutes"`
			Sections   []derivedSection `json:"sections"`
		}{
			MaterialID: artifact.MaterialID,
			SHA256:     artifact.SHA256,
			Version:    artifact.Version,
			Title:      artifact.Title,
			Chars:      artifact.Chars,
			Minutes:    artifact.Minutes,
		}
		for _, section := range artifact.Sections {
			path := section.Path
			if path == nil {
				path = []string{}
			}
			payload.Sections = append(payload.Sections, derivedSection{
				Anchor:    section.Anchor,
				StableKey: section.StableKey,
				Title:     section.Title,
				Level:     section.Level,
				Start:     section.Start,
				Path:      path,
				Body:      section.Body,
			})
		}
		if err := writeJSONFile(filepath.Join(dir, artifact.SHA256+".json"), payload); err != nil {
			state.fail("derived:"+artifact.MaterialID, "写入解析产物失败：%v", err)
		}
	}
	manifest := map[string]any{
		"version": version,
		"kind":    "content-index",
		"materials": func() []map[string]any {
			out := make([]map[string]any, 0, len(artifacts))
			for _, artifact := range artifacts {
				out = append(out, map[string]any{
					"id":       artifact.MaterialID,
					"sha256":   artifact.SHA256,
					"version":  artifact.Version,
					"sections": len(artifact.Sections),
				})
			}
			return out
		}(),
	}
	if err := writeJSONFile(filepath.Join(s.cfg.DerivedDir(), "content", version+".json"), manifest); err != nil {
		state.fail("derived:manifest", "写入索引清单失败：%v", err)
	}
}

func writeJSONFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".derived-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

const importLockKey int64 = 917_263_542

// ---------------------------------------------------------------- jobs

func (s *Service) createImportJob(ctx context.Context, actorID string) (string, error) {
	var actor any
	if actorID != "" {
		actor = actorID
	}
	var id string
	err := s.db.Pool.QueryRow(ctx, `
		insert into jobs (kind, status, created_by, started_at)
		values ('content_import', 'running', $1::uuid, now())
		returning id::text`, actor).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *Service) finishImportJob(ctx context.Context, jobID string, result ImportResult, errs []jobError, importErr error) {
	status := "completed"
	if len(errs) > 0 {
		status = "partial"
	}
	stats := map[string]any{"version": result.Version}
	for key, value := range result.Counts {
		stats[key] = value
	}
	if result.Skipped {
		stats["skipped"] = true
	}
	if importErr != nil {
		status = "failed"
		stats["error"] = importErr.Error()
	}
	if errs == nil {
		errs = []jobError{}
	}
	statsJSON, _ := json.Marshal(stats)
	errorsJSON, _ := json.Marshal(errs)
	// The job row must survive request cancellation: it is the operator's only
	// record of what happened.
	finishCtx := context.WithoutCancel(ctx)
	if _, err := s.db.Pool.Exec(finishCtx, `
		update jobs set status = $2, stats = $3::jsonb, errors = $4::jsonb, finished_at = now()
		where id = $1::uuid`, jobID, status, string(statsJSON), string(errorsJSON)); err != nil {
		s.log.Error("finish content import job", "job_id", jobID, "error", err)
	}
}

// CatalogVersion reports the currently published content version.
func (s *Service) CatalogVersion(ctx context.Context) (string, error) {
	var version string
	err := s.db.Pool.QueryRow(ctx, `select content_version from platform_state where id = 1`).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", store.MapError(err)
	}
	return version, nil
}

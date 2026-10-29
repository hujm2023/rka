package content

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"rka/internal/auth"
	"rka/internal/httpx"
)

type materialVersionView struct {
	ID          string     `json:"id"`
	Version     int        `json:"version"`
	SHA256      string     `json:"sha256"`
	Chars       int        `json:"chars"`
	Minutes     int        `json:"minutes"`
	Excerpt     string     `json:"excerpt"`
	IndexStatus string     `json:"indexStatus"`
	IndexError  string     `json:"indexError"`
	PublishedAt *time.Time `json:"publishedAt"`
	CreatedAt   time.Time  `json:"createdAt"`
	Current     bool       `json:"current"`
}

type materialVersionRow struct {
	ID          string
	Version     int
	SHA256      string
	Chars       int
	Minutes     int
	Excerpt     string
	IndexStatus string
	IndexError  string
	PublishedAt *time.Time
	CreatedAt   time.Time
	MediaType   string
	RelPath     string
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (s *Service) handleListMaterials(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	views, err := s.listMaterialViews(r.Context(), user, "")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"items": views})
}

func (s *Service) handleGetMaterial(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if !s.materialReadable(w, r, id) {
		return
	}
	user := auth.MustUser(ctx)
	views, err := s.listMaterialViews(ctx, user, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if len(views) == 0 {
		httpx.Error(w, r, httpx.ErrNotFound)
		return
	}
	versions, err := s.materialVersions(ctx, id, authUser(r).IsAdmin())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"material": views[0],
		"versions": versions,
	})
}

func (s *Service) handleMaterialVersions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if !s.materialReadable(w, r, id) {
		return
	}
	versions, err := s.materialVersions(ctx, id, authUser(r).IsAdmin())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"items": versions, "nextCursor": ""})
}

// handleMaterialRaw streams the immutable original with its markdown content
// type. `?version=` accepts either a version number or a version UUID; without
// it the currently published version is served.
func (s *Service) handleMaterialRaw(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if !s.materialReadable(w, r, id) {
		return
	}
	version, err := s.resolveMaterialVersion(ctx, id, r.URL.Query().Get("version"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !s.versionVisible(w, r, version) {
		return
	}
	path, err := s.originalPath(version.RelPath)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		s.log.Error("open original", "material", id, "path", path, "error", err)
		httpx.Error(w, r, httpx.NewError(http.StatusInternalServerError, "original_missing", "资料原件不可用。"))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.Header().Set("Content-Type", version.MediaType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", id+".md"))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, id+".md", info.ModTime(), file)
}

// handleMaterialContent returns the full markdown plus the section tree of the
// requested version, which is what the reader renders.
func (s *Service) handleMaterialContent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if !s.materialReadable(w, r, id) {
		return
	}
	version, err := s.resolveMaterialVersion(ctx, id, r.URL.Query().Get("version"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !s.versionVisible(w, r, version) {
		return
	}
	path, err := s.originalPath(version.RelPath)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		s.log.Error("read original", "material", id, "path", path, "error", err)
		httpx.Error(w, r, httpx.NewError(http.StatusInternalServerError, "original_missing", "资料原件不可用。"))
		return
	}
	// Content negotiation: plain markdown for readers that ask for it, JSON
	// (markdown + section tree) for the browser reader.
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "text/markdown") && !strings.Contains(accept, "application/json") {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(raw)
		return
	}
	sections, err := s.sectionsForVersions(ctx, []string{version.ID})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	list := sections[version.ID]
	if list == nil {
		list = []sectionView{}
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"materialId":        id,
		"materialVersionId": version.ID,
		"versionId":         version.ID,
		"currentVersionId":  version.ID,
		"version":           version.Version,
		"chars":             version.Chars,
		"minutes":           version.Minutes,
		"indexStatus":       version.IndexStatus,
		"publishedAt":       utcPointer(version.PublishedAt),
		"sections":          list,
		"content":           string(raw),
	})
}

// materialReadable writes the correct error envelope and reports whether the
// request may continue: 404 for unknown material, 403 for revoked access.
func (s *Service) materialReadable(w http.ResponseWriter, r *http.Request, materialID string) bool {
	ctx := r.Context()
	user := auth.MustUser(ctx)
	var exists bool
	if err := s.db.Pool.QueryRow(ctx, `select exists (select 1 from materials where id = $1)`, materialID).Scan(&exists); err != nil {
		httpx.Error(w, r, err)
		return false
	}
	if !exists {
		httpx.Error(w, r, httpx.ErrNotFound)
		return false
	}
	if !s.auth.CanAccessMaterial(ctx, user, materialID) {
		httpx.Error(w, r, httpx.ErrForbidden)
		return false
	}
	return true
}

func (s *Service) materialVersions(ctx context.Context, materialID string, includeUnpublished bool) ([]materialVersionView, error) {
	var current string
	_ = s.db.Pool.QueryRow(ctx, `select coalesce(current_version_id::text, '') from materials where id = $1`, materialID).Scan(&current)
	rows, err := s.db.Pool.Query(ctx, `
		select id::text, version, sha256, chars, minutes, excerpt, index_status, index_error,
		       published_at, created_at
		from material_versions where material_id = $1
		  and ($2 or published_at is not null)
		order by version desc`, materialID, includeUnpublished)
	if err != nil {
		return nil, fmt.Errorf("query material versions: %w", err)
	}
	defer rows.Close()
	versions := []materialVersionView{}
	for rows.Next() {
		var view materialVersionView
		if err := rows.Scan(&view.ID, &view.Version, &view.SHA256, &view.Chars, &view.Minutes,
			&view.Excerpt, &view.IndexStatus, &view.IndexError, &view.PublishedAt, &view.CreatedAt); err != nil {
			return nil, err
		}
		view.Current = view.ID == current
		view.CreatedAt = view.CreatedAt.UTC()
		view.PublishedAt = utcPointer(view.PublishedAt)
		versions = append(versions, view)
	}
	return versions, rows.Err()
}

// resolveMaterialVersion finds the requested version row. The version parameter
// is optional: a number selects material_versions.version, a UUID selects the
// row id, and an empty value selects the current published version.
func (s *Service) resolveMaterialVersion(ctx context.Context, materialID, raw string) (materialVersionRow, error) {
	raw = strings.TrimSpace(raw)
	var (
		row  materialVersionRow
		sql  string
		args []any
	)
	base := `
		select v.id::text, v.version, v.sha256, v.chars, v.minutes, v.excerpt,
		       v.index_status, v.index_error, v.published_at, v.created_at,
		       b.media_type, b.rel_path
		from material_versions v
		join blobs b on b.sha256 = v.sha256
		where v.material_id = $1`
	switch {
	case raw == "":
		sql = base + ` and v.id = (select current_version_id from materials where id = $1)`
		args = []any{materialID}
	case isDigits(raw):
		sql = base + ` and v.version = $2`
		args = []any{materialID, raw}
	case uuidPattern.MatchString(raw):
		sql = base + ` and v.id = $2::uuid`
		args = []any{materialID, raw}
	default:
		return materialVersionRow{}, httpx.NewError(http.StatusBadRequest, "bad_version", "版本参数不正确。")
	}
	err := s.db.Pool.QueryRow(ctx, sql, args...).Scan(&row.ID, &row.Version, &row.SHA256, &row.Chars,
		&row.Minutes, &row.Excerpt, &row.IndexStatus, &row.IndexError, &row.PublishedAt, &row.CreatedAt,
		&row.MediaType, &row.RelPath)
	if errors.Is(err, pgx.ErrNoRows) {
		return materialVersionRow{}, httpx.NewError(http.StatusNotFound, "version_not_found", "资料版本不存在。")
	}
	if err != nil {
		return materialVersionRow{}, fmt.Errorf("query material version: %w", err)
	}
	return row, nil
}

// originalPath resolves a blobs.rel_path under the originals root and refuses
// paths that would escape it.
func (s *Service) originalPath(relPath string) (string, error) {
	root := filepath.Clean(s.cfg.OriginalsDir())
	full := filepath.Join(root, filepath.FromSlash(relPath))
	if full != root && !strings.HasPrefix(full, root+string(os.PathSeparator)) {
		return "", httpx.NewError(http.StatusInternalServerError, "invalid_original_path", "原件路径不正确。")
	}
	return full, nil
}

// versionVisible hides unpublished versions (mid-import or failed builds) from
// learners while administrators may inspect them.
func (s *Service) versionVisible(w http.ResponseWriter, r *http.Request, version materialVersionRow) bool {
	if authUser(r).IsAdmin() || version.PublishedAt != nil {
		return true
	}
	httpx.Error(w, r, httpx.NewError(http.StatusNotFound, "version_not_found", "资料版本不存在。"))
	return false
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

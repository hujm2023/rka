package ops

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"rka/internal/auth"
	"rka/internal/httpx"
)

func (s *Service) handleListBackups(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 200 {
			limit = parsed
		}
	}
	rows, err := s.db.Pool.Query(r.Context(), `
		select b.id::text, b.rel_path, b.size_bytes, b.sha256, b.status, b.created_at,
		       coalesce(u.email, ''), b.manifest
		from backups b left join users u on u.id = b.created_by
		order by b.created_at desc, b.id desc
		limit $1`, limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	for rows.Next() {
		var id, relPath, sha, status, email string
		var size int64
		var createdAt time.Time
		var manifestRaw []byte
		if err := rows.Scan(&id, &relPath, &size, &sha, &status, &createdAt, &email, &manifestRaw); err != nil {
			httpx.Error(w, r, err)
			return
		}
		item := map[string]any{
			"id": id, "relPath": relPath, "path": filepath.Join(s.cfg.DataDir, filepath.FromSlash(relPath)),
			"sizeBytes": size, "sha256": sha, "status": status, "createdAt": createdAt.UTC(),
			"createdBy": email,
		}
		var manifest backupManifest
		if json.Unmarshal(manifestRaw, &manifest) == nil {
			item["versions"] = map[string]any{
				"binaryVersion":   manifest.BinaryVersion,
				"dbSchemaVersion": manifest.DBSchemaVersion,
				"contentVersion":  manifest.ContentVersion,
			}
			item["dbDumpSha256"] = manifest.DBDumpSHA256
			item["tables"] = manifest.Tables
			item["lastVerify"] = manifest.LastVerify
			item["error"] = manifest.Error
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"items": items, "nextCursor": ""})
}

func (s *Service) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	// A backup may take minutes; keep it running even if the browser disconnects.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Minute)
	defer cancel()
	backup, err := s.CreateBackup(ctx, user.ID)
	if err != nil {
		var apiError *httpx.APIError
		if errors.As(err, &apiError) {
			httpx.Error(w, r, err)
		} else {
			httpx.Error(w, r, httpx.NewError(http.StatusInternalServerError, "backup_failed", "创建备份失败："+err.Error()))
		}
		return
	}
	s.record(r, "backup.created", "backup", backup.ID, map[string]any{
		"relPath": backup.RelPath, "sizeBytes": backup.Size, "sha256": backup.SHA256,
	})
	httpx.JSON(w, r, http.StatusCreated, map[string]any{"backup": backup})
}

func (s *Service) handleVerifyBackup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Minute)
	defer cancel()
	report, err := s.VerifyBackup(ctx, id)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.record(r, "backup.verified", "backup", report.ID, map[string]any{
		"ok": report.OK, "checks": len(report.Checks), "errors": len(report.Errors),
	})
	httpx.JSON(w, r, http.StatusOK, map[string]any{"report": report, "ok": report.OK})
}

type restoreBackupRequest struct {
	TargetDir string `json:"targetDir"`
}

func (s *Service) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var request restoreBackupRequest
	if err := httpx.Decode(w, r, &request, 8<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Minute)
	defer cancel()
	result, err := s.Restore(ctx, id, request.TargetDir)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.record(r, "backup.restored", "backup", id, map[string]any{
		"targetDir": result.TargetDir, "originals": result.Originals,
	})
	httpx.JSON(w, r, http.StatusOK, map[string]any{"restore": result, "targetDir": result.TargetDir})
}

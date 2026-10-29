package writing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"rka/internal/auth"
	"rka/internal/httpx"
)

const (
	// MaxValuesBytes caps a single serialised draft payload. A draft larger
	// than this is rejected before it can reach PostgreSQL.
	MaxValuesBytes = 200 << 10
	// maxDocumentBody is slightly above MaxValuesBytes so the explicit limit
	// produces the stable "too_large" error instead of a generic body error.
	maxDocumentBody = 2 << 20
)

// rowQuerier is implemented by both *pgxpool.Pool and pgx.Tx.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// documentRecord is the internal projection of writing_documents.
type documentRecord struct {
	ID        string
	Kind      string
	TargetID  string
	Values    map[string]string
	Version   int
	UpdatedAt time.Time
}

type documentView struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	TargetID  string            `json:"targetId"`
	Values    map[string]string `json:"values"`
	Version   int               `json:"version"`
	UpdatedAt *time.Time        `json:"updatedAt"`
}

func viewOfDocument(rec documentRecord) documentView {
	updatedAt := rec.UpdatedAt.UTC()
	return documentView{
		ID:        rec.ID,
		Kind:      rec.Kind,
		TargetID:  rec.TargetID,
		Values:    rec.Values,
		Version:   rec.Version,
		UpdatedAt: &updatedAt,
	}
}

// emptyDocument is returned when the target exists but this user has not saved
// a draft yet. Version 0 lets the client create the document with a normal
// optimistic-concurrency check.
func emptyDocument(kind, targetID string) documentView {
	return documentView{
		ID:       "",
		Kind:     kind,
		TargetID: targetID,
		Values:   map[string]string{},
		Version:  0,
	}
}

// resolveTarget validates the path parameters shared by every writing route.
func (s *Service) resolveTarget(r *http.Request) (kind, targetID string, err error) {
	kind = r.PathValue("kind")
	if kind != "case" && kind != "essay" {
		return "", "", httpx.NewError(http.StatusBadRequest, "invalid_kind", "写作类型只能是 case 或 essay。")
	}
	targetID = r.PathValue("targetId")
	if targetID == "" {
		return "", "", httpx.NewError(http.StatusBadRequest, "invalid_target", "写作题目标识不能为空。")
	}
	var exists bool
	if err := s.db.Pool.QueryRow(r.Context(),
		`select exists (select 1 from subjective_items where id = $1 and kind = $2)`,
		targetID, kind).Scan(&exists); err != nil {
		return "", "", fmt.Errorf("resolve writing target: %w", err)
	}
	if !exists {
		return "", "", httpx.NewError(http.StatusNotFound, "not_found", "写作题目不存在。")
	}
	return kind, targetID, nil
}

func decodeValues(raw []byte) (map[string]string, error) {
	values := map[string]string{}
	if len(raw) == 0 {
		return values, nil
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("decode writing values: %w", err)
	}
	return values, nil
}

// loadDocumentRow reads this user's document for one target. forUpdate locks
// the row inside a transaction so conflict snapshots see a stable version.
func loadDocumentRow(ctx context.Context, q rowQuerier, userID, kind, targetID string, forUpdate bool) (documentRecord, bool, error) {
	sql := `
		select id::text, values, version, updated_at
		from writing_documents
		where user_id = $1::uuid and kind = $2 and target_id = $3`
	if forUpdate {
		sql += " for update"
	}
	var (
		rec documentRecord
		raw []byte
	)
	err := q.QueryRow(ctx, sql, userID, kind, targetID).Scan(&rec.ID, &raw, &rec.Version, &rec.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return documentRecord{}, false, nil
	}
	if err != nil {
		return documentRecord{}, false, fmt.Errorf("load writing document: %w", err)
	}
	rec.Kind = kind
	rec.TargetID = targetID
	values, err := decodeValues(raw)
	if err != nil {
		return documentRecord{}, false, err
	}
	rec.Values = values
	return rec, true, nil
}

func appendVersionTx(ctx context.Context, tx pgx.Tx, documentID string, version int, encoded []byte, source, userID string) error {
	if _, err := tx.Exec(ctx, `
		insert into writing_versions (document_id, version, values, source, created_by)
		values ($1::uuid, $2, $3::jsonb, $4, $5::uuid)`,
		documentID, version, encoded, source, userID); err != nil {
		return fmt.Errorf("append writing version: %w", err)
	}
	return nil
}

// recordConflictSnapshotTx keeps both sides of a rejected write. Server content
// is stored under the version it was read at, the rejected client content under
// the version the client tried to write. The caller holds the document row
// lock; equal payloads are deduplicated, distinct rejected edits are append-only.
func recordConflictSnapshotTx(ctx context.Context, tx pgx.Tx, documentID string, serverVersion int, serverValues map[string]string, clientVersion int, clientValues map[string]string, userID string) error {
	record := func(version int, values map[string]string) error {
		encoded, err := json.Marshal(values)
		if err != nil {
			return fmt.Errorf("encode conflict values: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			insert into writing_versions (document_id, version, values, source, created_by)
			select $1::uuid, $2, $3::jsonb, 'conflict', $4::uuid
            where not exists (select 1 from writing_versions v
                where v.document_id=$1::uuid and v.version=$2 and v.source='conflict' and v.values=$3::jsonb)`,
			documentID, version, encoded, userID); err != nil {
			return fmt.Errorf("record conflict version: %w", err)
		}
		return nil
	}
	if err := record(serverVersion, serverValues); err != nil {
		return err
	}
	if clientVersion != serverVersion {
		if err := record(clientVersion, clientValues); err != nil {
			return err
		}
	}
	return nil
}

func updatedAtPointer(value time.Time) *time.Time {
	utc := value.UTC()
	return &utc
}

func conflictError(serverVersion int, serverValues, clientValues map[string]string) *httpx.APIError {
	return httpx.ErrConflict.WithDetails(map[string]any{
		"serverVersion": serverVersion,
		"serverValues":  serverValues,
		"clientValues":  clientValues,
	})
}

// saveDocument applies one version-checked write. A nil APIError means success;
// a non-nil one is the 409 payload and the write was rolled back (only the
// conflict snapshot is kept, and that transaction commits).
func (s *Service) saveDocument(ctx context.Context, userID, kind, targetID string, values map[string]string, encoded []byte, clientVersion int) (documentView, *httpx.APIError, error) {
	var (
		view     documentView
		conflict *httpx.APIError
	)
	err := s.db.Tx(ctx, func(tx pgx.Tx) error {
		if clientVersion == 0 {
			var (
				id        string
				version   int
				updatedAt time.Time
			)
			err := tx.QueryRow(ctx, `
				insert into writing_documents (user_id, kind, target_id, values, version)
				values ($1::uuid, $2, $3, $4::jsonb, 1)
				on conflict (user_id, kind, target_id) do nothing
				returning id::text, version, updated_at`,
				userID, kind, targetID, encoded).Scan(&id, &version, &updatedAt)
			if errors.Is(err, pgx.ErrNoRows) {
				// Another device created the document first: never overwrite it.
				current, found, findErr := loadDocumentRow(ctx, tx, userID, kind, targetID, true)
				if findErr != nil {
					return findErr
				}
				if !found {
					return fmt.Errorf("writing document conflicted but is not readable")
				}
				if recordErr := recordConflictSnapshotTx(ctx, tx, current.ID, current.Version, current.Values, clientVersion, values, userID); recordErr != nil {
					return recordErr
				}
				conflict = conflictError(current.Version, current.Values, values)
				return nil
			}
			if err != nil {
				return fmt.Errorf("insert writing document: %w", err)
			}
			if err := appendVersionTx(ctx, tx, id, version, encoded, "user", userID); err != nil {
				return err
			}
			view = documentView{ID: id, Kind: kind, TargetID: targetID, Values: values, Version: version, UpdatedAt: updatedAtPointer(updatedAt)}
			return nil
		}

		var (
			id        string
			version   int
			updatedAt time.Time
		)
		err := tx.QueryRow(ctx, `
			update writing_documents
			set values = $1::jsonb, version = version + 1, updated_at = now()
			where user_id = $2::uuid and kind = $3 and target_id = $4 and version = $5
			returning id::text, version, updated_at`,
			encoded, userID, kind, targetID, clientVersion).Scan(&id, &version, &updatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			current, found, findErr := loadDocumentRow(ctx, tx, userID, kind, targetID, true)
			if findErr != nil {
				return findErr
			}
			serverVersion := 0
			serverValues := map[string]string{}
			if found {
				serverVersion = current.Version
				serverValues = current.Values
				if recordErr := recordConflictSnapshotTx(ctx, tx, current.ID, current.Version, current.Values, clientVersion, values, userID); recordErr != nil {
					return recordErr
				}
			}
			conflict = conflictError(serverVersion, serverValues, values)
			return nil
		}
		if err != nil {
			return fmt.Errorf("update writing document: %w", err)
		}
		if err := appendVersionTx(ctx, tx, id, version, encoded, "user", userID); err != nil {
			return err
		}
		view = documentView{ID: id, Kind: kind, TargetID: targetID, Values: values, Version: version, UpdatedAt: updatedAtPointer(updatedAt)}
		return nil
	})
	if err != nil {
		return documentView{}, nil, err
	}
	return view, conflict, nil
}

// ---------------------------------------------------------------- handlers

func (s *Service) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	kind, targetID, err := s.resolveTarget(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	rec, found, err := loadDocumentRow(r.Context(), s.db.Pool, user.ID, kind, targetID, false)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !found {
		httpx.JSON(w, r, http.StatusOK, map[string]any{"document": emptyDocument(kind, targetID)})
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"document": viewOfDocument(rec)})
}

type putDocumentRequest struct {
	Values  map[string]string `json:"values"`
	Version *int              `json:"version"`
}

func (s *Service) handlePutDocument(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	kind, targetID, err := s.resolveTarget(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var req putDocumentRequest
	if err := httpx.Decode(w, r, &req, maxDocumentBody); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if req.Version == nil {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "version_required", "保存草稿必须携带版本号。"))
		return
	}
	if *req.Version < 0 {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "invalid_version", "版本号不能为负数。"))
		return
	}
	if req.Values == nil {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "values_required", "请提交草稿内容。"))
		return
	}
	encoded, err := json.Marshal(req.Values)
	if err != nil {
		httpx.Error(w, r, fmt.Errorf("encode writing values: %w", err))
		return
	}
	if len(encoded) > MaxValuesBytes {
		httpx.Error(w, r, httpx.NewError(http.StatusRequestEntityTooLarge, "too_large", "草稿内容超过 200KB 上限。"))
		return
	}
	view, conflict, err := s.saveDocument(r.Context(), user.ID, kind, targetID, req.Values, encoded, *req.Version)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if conflict != nil {
		httpx.Error(w, r, conflict)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{"document": view})
}

type versionSummary struct {
	ID        string    `json:"id"`
	Version   int       `json:"version"`
	Source    string    `json:"source"`
	CreatedBy *string   `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

type versionDetail struct {
	ID        string            `json:"id"`
	Version   int               `json:"version"`
	Source    string            `json:"source"`
	Values    map[string]string `json:"values"`
	CreatedBy *string           `json:"createdBy"`
	CreatedAt time.Time         `json:"createdAt"`
}

func (s *Service) handleListVersions(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	kind, targetID, err := s.resolveTarget(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	rec, found, err := loadDocumentRow(r.Context(), s.db.Pool, user.ID, kind, targetID, false)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := []versionSummary{}
	if found {
		rows, err := s.db.Pool.Query(r.Context(), `
			select id::text, version, source, created_by::text, created_at
			from writing_versions
			where document_id = $1::uuid
			order by version desc,
			         case when source = 'user' then 0 else 1 end,
			         created_at desc`, rec.ID)
		if err != nil {
			httpx.Error(w, r, fmt.Errorf("list writing versions: %w", err))
			return
		}
		defer rows.Close()
		for rows.Next() {
			var item versionSummary
			if err := rows.Scan(&item.ID, &item.Version, &item.Source, &item.CreatedBy, &item.CreatedAt); err != nil {
				httpx.Error(w, r, fmt.Errorf("scan writing version: %w", err))
				return
			}
			item.CreatedAt = item.CreatedAt.UTC()
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			httpx.Error(w, r, fmt.Errorf("list writing versions: %w", err))
			return
		}
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"items":          items,
		"nextCursor":     "",
		"currentVersion": rec.Version,
	})
}

func (s *Service) handleGetVersion(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	kind, targetID, err := s.resolveTarget(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	raw := r.PathValue("version")
	version, convErr := strconv.Atoi(raw)
	if convErr != nil || version < 0 {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "invalid_version", "版本号不正确。"))
		return
	}
	source := r.URL.Query().Get("source")
	if source != "" && source != "user" && source != "conflict" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "invalid_source", "版本来源只能是 user 或 conflict。"))
		return
	}
	rec, found, err := loadDocumentRow(r.Context(), s.db.Pool, user.ID, kind, targetID, false)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !found {
		httpx.Error(w, r, httpx.NewError(http.StatusNotFound, "not_found", "该版本不存在。"))
		return
	}
	if source == "" {
		source = "user"
	}
	var (
		item    versionDetail
		values  []byte
		created time.Time
	)
	query := `
		select id::text, version, source, values, created_by::text, created_at
		from writing_versions
		where document_id = $1::uuid and version = $2 and source = $3
        and ($4 = '' or id::text = $4)
        order by created_at desc, id desc limit 1`
	err = s.db.Pool.QueryRow(r.Context(), query, rec.ID, version, source, r.URL.Query().Get("snapshotId")).
		Scan(&item.ID, &item.Version, &item.Source, &values, &item.CreatedBy, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.Error(w, r, httpx.NewError(http.StatusNotFound, "not_found", "该版本不存在。"))
		return
	}
	if err != nil {
		httpx.Error(w, r, fmt.Errorf("load writing version: %w", err))
		return
	}
	item.Values, err = decodeValues(values)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	item.CreatedAt = created.UTC()
	httpx.JSON(w, r, http.StatusOK, map[string]any{"version": item})
}

package teaching

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"rka/internal/auth"
	"rka/internal/httpx"
)

type knowledgeIndex struct {
	ID         string `json:"id"`
	MaterialID string `json:"materialId"`
	VersionID  string `json:"versionId"`
	Title      string `json:"title"`
	Name       string `json:"-"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	Path       string `json:"-"`
	SHA        string `json:"-"`
}

// ProcessKnowledgeIndex performs a bounded step, never waits for an entire
// remote build. Deterministic names let restart/retry query an accepted build
// rather than upload duplicate copies. Older ready versions are kept intact.
func (s *Service) ProcessKnowledgeIndex(ctx context.Context) (bool, error) {
	d, ok := s.deepTutor.(*deepTutorWSBackend)
	if !ok {
		return false, nil
	}
	fingerprint := knowledgeFingerprint(s.cfg)
	_, err := s.db.Pool.Exec(ctx, `insert into knowledge_indexes(material_version_id,fingerprint,kb_name)
  select v.id,$1,'rka_'||replace(v.id::text,'-','')||'_'||$1 from material_versions v
  join materials m on m.id=v.material_id where v.index_status='ready'
  on conflict(material_version_id,fingerprint) do nothing`, fingerprint)
	if err != nil {
		return false, err
	}
	if err = s.ConfigureNative(ctx); err != nil {
		_, _ = s.db.Pool.Exec(ctx, `update knowledge_indexes set error=$1 where fingerprint=$2 and status='queued'`, err.Error(), fingerprint)
		return false, err
	}
	owner := randomHex(12)
	var index knowledgeIndex
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select k.id::text,m.id,v.id::text,m.title,k.kb_name,k.status,k.error,b.rel_path,b.sha256
   from knowledge_indexes k join material_versions v on v.id=k.material_version_id
   join materials m on m.id=v.material_id join blobs b on b.sha256=v.sha256
   where k.fingerprint=$1 and k.status in ('queued','indexing') and (k.lease_until is null or k.lease_until<now())
   order by k.updated_at limit 1 for update of k skip locked`, fingerprint).
			Scan(&index.ID, &index.MaterialID, &index.VersionID, &index.Title, &index.Name, &index.Status, &index.Error, &index.Path, &index.SHA)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `update knowledge_indexes set status='indexing',lease_owner=$2,lease_until=now()+interval '90 seconds',updated_at=now() where id=$1::uuid`, index.ID, owner)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	state, message := s.advanceKnowledgeIndex(callCtx, d, index)
	bookCtx, bookCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer bookCancel()
	_, err = s.db.Pool.Exec(bookCtx, `update knowledge_indexes set status=$3,error=$4,lease_owner='',lease_until=case when $3='indexing' then now()+interval '10 seconds' else null end,updated_at=now() where id=$1::uuid and lease_owner=$2`, index.ID, owner, state, message)
	return true, err
}

func (s *Service) advanceKnowledgeIndex(ctx context.Context, d *deepTutorWSBackend, index knowledgeIndex) (string, string) {
	token, err := d.adminToken(ctx)
	if err != nil {
		return "indexing", err.Error()
	}
	var info struct {
		Status     string `json:"status"`
		Statistics struct {
			Ready        bool `json:"rag_initialized"`
			Active       bool `json:"active_match"`
			NeedsReindex bool `json:"needs_reindex"`
			Files        int  `json:"raw_documents"`
		} `json:"statistics"`
	}
	path := "/api/knowledge-bases/" + url.PathEscape(index.Name)
	err = d.jsonCall(ctx, http.MethodGet, path, nil, token, &info)
	if err == nil {
		if info.Status == "ready" && info.Statistics.Ready && info.Statistics.Active && !info.Statistics.NeedsReindex && info.Statistics.Files > 0 {
			return "ready", ""
		}
		var progress struct {
			Stage   string `json:"stage"`
			Message string `json:"message"`
		}
		if e := d.jsonCall(ctx, http.MethodGet, path+"/progress", nil, token, &progress); e != nil {
			return "indexing", e.Error()
		}
		if progress.Stage == "error" || info.Status == "failed" || info.Status == "error" {
			return "failed", progress.Message
		}
		return "indexing", progress.Message
	}
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Status != http.StatusNotFound {
		return "indexing", err.Error()
	}
	clean := filepath.Clean(index.Path)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "failed", "原件路径不安全"
	}
	data, err := os.ReadFile(filepath.Join(s.cfg.OriginalsDir(), strings.TrimPrefix(clean, "originals/")))
	if err != nil {
		return "failed", err.Error()
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != index.SHA {
		return "failed", "原件校验失败，拒绝创建检索索引"
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for key, value := range map[string]string{"name": index.Name, "rag_provider": "llamaindex"} {
		if err := form.WriteField(key, value); err != nil {
			return "failed", err.Error()
		}
	}
	if s.cfg.Teaching.EmbeddingURL != "" {
		selection, _ := json.Marshal(map[string]string{"profile_id": "rka-embedding-" + knowledgeFingerprint(s.cfg), "model_id": "rka-embedding-" + knowledgeFingerprint(s.cfg)})
		if err := form.WriteField("embedding_model", string(selection)); err != nil {
			return "failed", err.Error()
		}
	}
	file, err := form.CreateFormFile("files", index.MaterialID+"-"+index.VersionID+".md")
	if err != nil {
		return "failed", err.Error()
	}
	if _, err = file.Write(data); err != nil {
		return "failed", err.Error()
	}
	if err = form.Close(); err != nil {
		return "failed", err.Error()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, d.baseURL+"/api/knowledge-bases", &body)
	if err != nil {
		return "failed", err.Error()
	}
	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := d.client.Do(request)
	if err != nil {
		return "indexing", "上传结果待核对：" + err.Error()
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return "indexing", "已上传原件，等待语义索引构建与验证"
	}
	message, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
	return "failed", fmt.Sprintf("DeepTutor 索引请求返回 %d：%s", response.StatusCode, string(message))
}

const knowledgeAccess = `(m.review_status='published' or exists(select 1 from content_grants g where g.user_id=$1::uuid and g.resource_type='material' and g.resource_id=m.id and g.revoked_at is null) or exists(select 1 from users u where u.id=$1::uuid and u.role='admin' and u.status='active'))`

func (s *Service) RegisterKnowledge(mux *http.ServeMux, identity *auth.Service) {
	mux.HandleFunc("GET /api/content/indexes", identity.Require(func(w http.ResponseWriter, r *http.Request) {
		user := auth.MustUser(r.Context())
		rows, err := s.db.Pool.Query(r.Context(), `select k.id::text,m.id,v.id::text,m.title,k.status,k.error from knowledge_indexes k join material_versions v on v.id=k.material_version_id join materials m on m.id=v.material_id where `+knowledgeAccess+` order by m.id,v.version desc,k.created_at desc`, user.ID)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		defer rows.Close()
		items := []knowledgeIndex{}
		for rows.Next() {
			var item knowledgeIndex
			if err := rows.Scan(&item.ID, &item.MaterialID, &item.VersionID, &item.Title, &item.Status, &item.Error); err != nil {
				httpx.Error(w, r, err)
				return
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			httpx.Error(w, r, err)
			return
		}
		httpx.JSON(w, r, http.StatusOK, map[string]any{"items": items, "configured": s.cfg.Teaching.DeepTutorURL != ""})
	}))
	mux.HandleFunc("POST /api/admin/content/indexes/{id}/retry", identity.RequireAdmin(func(w http.ResponseWriter, r *http.Request) {
		// Retry in a new namespace: do not overwrite an index still used by citations.
		tag, err := s.db.Pool.Exec(r.Context(), `update knowledge_indexes set status='queued',error='',lease_until=null,kb_name='rka_'||replace(material_version_id::text,'-','')||'_'||fingerprint||'_'||$2,updated_at=now() where id=$1::uuid and status='failed'`, r.PathValue("id"), randomHex(3))
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		if tag.RowsAffected() == 0 {
			httpx.Error(w, r, httpx.NewError(http.StatusConflict, "index_not_failed", "索引不存在或没有失败。"))
			return
		}
		httpx.JSON(w, r, http.StatusOK, map[string]any{"status": "queued"})
	}))
}

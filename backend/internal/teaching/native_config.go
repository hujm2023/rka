package teaching

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"rka/internal/config"
)

func (d *deepTutorWSBackend) jsonCall(ctx context.Context, method, path string, body any, token string, out any) error {
	response, err := d.doJSON(ctx, method, path, body, token)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		status := response.StatusCode
		// DeepTutor 1.7 wraps a missing knowledge base in HTTP 500. Normalize
		// only this exact resource error; unrelated server failures must never
		// trigger a new upload or be reported as a successfully built index.
		const prefix = "/api/knowledge-bases/"
		if status == http.StatusInternalServerError && method == http.MethodGet && strings.HasPrefix(path, prefix) {
			name, decodeErr := url.PathUnescape(strings.TrimPrefix(path, prefix))
			var failure struct {
				Detail string `json:"detail"`
			}
			if decodeErr == nil && name != "" && !strings.Contains(name, "/") &&
				json.NewDecoder(io.LimitReader(response.Body, 2048)).Decode(&failure) == nil &&
				failure.Detail == fmt.Sprintf("Knowledge base '%s' not found", name) {
				status = http.StatusNotFound
			}
		}
		return &UpstreamError{Code: "teaching_api_failed", Message: fmt.Sprintf("DeepTutor %s 返回 %d", path, status), Status: status}
	}
	if out != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(out)
	}
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<20))
	return err
}

func knowledgeFingerprint(cfg *config.Config) string {
	value := fmt.Sprintf("llamaindex:%s:%s:%d", cfg.Teaching.EmbeddingURL, cfg.Teaching.EmbeddingModel, cfg.Teaching.EmbeddingDims)
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:8])
}

// Only RKA-owned profiles are merged. Existing manual profiles and credentials
// remain intact; old embedding profiles remain addressable by old index versions.
func (s *Service) ConfigureNative(ctx context.Context) error {
	d, ok := s.deepTutor.(*deepTutorWSBackend)
	if !ok {
		return nil
	}
	s.nativeMu.Lock()
	defer s.nativeMu.Unlock()
	signature := knowledgeFingerprint(s.cfg) + s.cfg.AI.Endpoint + s.cfg.AI.Model + s.cfg.AI.APIKey
	if s.nativeSignature == signature {
		return nil
	}
	admin, err := d.adminToken(ctx)
	if err != nil {
		return err
	}
	var wrapper struct {
		Catalog map[string]any `json:"catalog"`
	}
	if err := d.jsonCall(ctx, http.MethodGet, "/api/settings/catalog", nil, admin, &wrapper); err != nil {
		return err
	}
	services, _ := wrapper.Catalog["services"].(map[string]any)
	if services == nil {
		services = map[string]any{}
	}
	apply := func(service, id, provider, endpoint, key, model string, dimension int) error {
		if endpoint == "" || model == "" {
			return nil
		}
		bucket, _ := services[service].(map[string]any)
		if bucket == nil {
			bucket = map[string]any{}
		}
		profiles, _ := bucket["profiles"].([]any)
		kept := []any{}
		for _, raw := range profiles {
			profile, _ := raw.(map[string]any)
			if profile["id"] != id {
				kept = append(kept, raw)
			}
		}
		entry := map[string]any{"id": id, "model": model, "name": model}
		if dimension > 0 {
			entry["dimension"] = dimension
			entry["send_dimensions"] = false
		}
		profile := map[string]any{"id": id, "name": "RKA", "provider": provider, "base_url": endpoint, "api_key": key, "models": []any{entry}, "api_format": "openai_chat", "wire_api": "chat_completions"}
		bucket["profiles"] = append(kept, profile)
		bucket["active_profile_id"] = id
		bucket["active_model_id"] = id
		return d.jsonCall(ctx, http.MethodPost, "/api/settings/apply/service", map[string]any{"service": service, "config": bucket}, admin, nil)
	}
	if err := apply("embedding", "rka-embedding-"+knowledgeFingerprint(s.cfg), "vllm", s.cfg.Teaching.EmbeddingURL, "local", s.cfg.Teaching.EmbeddingModel, s.cfg.Teaching.EmbeddingDims); err != nil {
		return err
	}
	if err := apply("llm", "rka-chat", "openai", strings.TrimSuffix(strings.TrimRight(s.cfg.AI.Endpoint, "/"), "/chat/completions"), s.cfg.AI.APIKey, s.cfg.AI.Model, 0); err != nil {
		return err
	}
	if s.cfg.Teaching.EmbeddingURL != "" {
		if err := d.jsonCall(ctx, http.MethodPut, "/api/knowledge-bases/rag-pipelines/llamaindex/config", map[string]any{"retrieval_profile": "hybrid"}, admin, nil); err != nil {
			return err
		}
	}
	s.nativeSignature = signature
	return nil
}

func (d *deepTutorWSBackend) activeModel(ctx context.Context, token string) (string, string, error) {
	var data struct {
		Catalog struct {
			Services struct {
				LLM struct {
					Profile string `json:"active_profile_id"`
					Model   string `json:"active_model_id"`
				} `json:"llm"`
			} `json:"services"`
		} `json:"catalog"`
	}
	if err := d.jsonCall(ctx, http.MethodGet, "/api/settings/catalog", nil, token, &data); err != nil {
		return "", "", err
	}
	return data.Catalog.Services.LLM.Profile, data.Catalog.Services.LLM.Model, nil
}

package admin

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"rka/internal/auth"
	"rka/internal/httpx"
)

// modelConfigRow is the internal projection; CredentialCiphertext is never
// serialised to the browser.
type modelConfigRow struct {
	ID               string
	Name             string
	Kind             string
	Provider         string
	Endpoint         string
	Model            string
	CredentialCipher string
	AuthConfigured   bool
	ConfigStatus     string
	ConnectionStatus string
	CapabilityStatus string
	Capabilities     []byte
	LastCheckedAt    *time.Time
	LastError        string
	IsDefault        bool
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

const modelConfigSelect = `
	select id::text, name, kind, provider, endpoint, model, credential_ciphertext,
	       auth_configured, config_status, connection_status, capability_status,
	       capabilities, last_checked_at, last_error, is_default, created_at, updated_at
	from model_configs`

func scanModelConfig(scanner interface{ Scan(...any) error }) (modelConfigRow, error) {
	var row modelConfigRow
	err := scanner.Scan(&row.ID, &row.Name, &row.Kind, &row.Provider, &row.Endpoint, &row.Model,
		&row.CredentialCipher, &row.AuthConfigured, &row.ConfigStatus, &row.ConnectionStatus,
		&row.CapabilityStatus, &row.Capabilities, &row.LastCheckedAt, &row.LastError,
		&row.IsDefault, &row.CreatedAt, &row.UpdatedAt)
	return row, err
}

func (row modelConfigRow) browserView() map[string]any {
	var capabilities any = map[string]any{}
	if len(row.Capabilities) > 0 {
		_ = json.Unmarshal(row.Capabilities, &capabilities)
	}
	return map[string]any{
		"id": row.ID, "name": row.Name, "kind": row.Kind, "provider": row.Provider,
		"endpoint": row.Endpoint, "model": row.Model,
		"authConfigured":    row.AuthConfigured,
		"credentialPresent": strings.TrimSpace(row.CredentialCipher) != "",
		"configured":        row.ConfigStatus == "configured",
		"configStatus":      row.ConfigStatus,
		"connectionStatus":  row.ConnectionStatus,
		"capabilityStatus":  row.CapabilityStatus,
		"capabilities":      capabilities,
		"lastCheckedAt":     row.LastCheckedAt,
		"lastError":         row.LastError,
		"isDefault":         row.IsDefault,
		"createdAt":         row.CreatedAt,
		"updatedAt":         row.UpdatedAt,
	}
}

func (s *Service) handleListModelConfigs(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Pool.Query(r.Context(), modelConfigSelect+` order by kind, is_default desc, created_at`)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		row, err := scanModelConfig(rows)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		items = append(items, row.browserView())
	}
	if err := rows.Err(); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"items": items,
		"environment": map[string]any{
			"configured":     strings.TrimSpace(s.cfg.AI.Endpoint) != "",
			"endpoint":       s.cfg.AI.Endpoint,
			"model":          s.cfg.AI.Model,
			"authConfigured": strings.TrimSpace(s.cfg.AI.APIKey) != "",
			"gatewayName":    s.cfg.AI.GatewayName,
		},
	})
}

type updateModelConfigRequest struct {
	Name            *string        `json:"name"`
	Provider        *string        `json:"provider"`
	Endpoint        *string        `json:"endpoint"`
	Model           *string        `json:"model"`
	Credential      *string        `json:"credential"`
	APIKey          *string        `json:"apiKey"`
	ClearCredential *bool          `json:"clearCredential"`
	IsDefault       *bool          `json:"isDefault"`
	Capabilities    map[string]any `json:"capabilities"`
}

func (s *Service) handleUpdateModelConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var request updateModelConfigRequest
	if err := httpx.Decode(w, r, &request, 32<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	row, err := scanModelConfig(s.db.Pool.QueryRow(r.Context(), modelConfigSelect+` where id = $1::uuid`, id))
	if err != nil {
		httpx.Error(w, r, httpx.ErrNotFound)
		return
	}

	name := row.Name
	if request.Name != nil {
		name = strings.TrimSpace(*request.Name)
		if name == "" || len(name) > 120 {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "invalid_name", "配置名称不能为空且不能超过 120 个字符。"))
			return
		}
	}
	provider := row.Provider
	if request.Provider != nil {
		provider = strings.TrimSpace(*request.Provider)
	}
	endpoint := row.Endpoint
	if request.Endpoint != nil {
		endpoint = strings.TrimSpace(*request.Endpoint)
	}
	if endpoint != "" {
		if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "invalid_endpoint", "endpoint 必须以 http:// 或 https:// 开头。"))
			return
		}
		if parsed, err := url.Parse(endpoint); err != nil || parsed.User != nil {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "invalid_endpoint", "endpoint 不能包含用户名或密码，请使用 credential 字段保存凭据。"))
			return
		}
	}
	model := row.Model
	if request.Model != nil {
		model = strings.TrimSpace(*request.Model)
	}

	credentialCipher := row.CredentialCipher
	authConfigured := row.AuthConfigured
	credentialChanged := false
	if request.ClearCredential != nil && *request.ClearCredential {
		credentialCipher = ""
		authConfigured = false
		credentialChanged = true
	}
	credentialInput := request.Credential
	if credentialInput == nil {
		credentialInput = request.APIKey
	}
	if credentialInput != nil && strings.TrimSpace(*credentialInput) != "" {
		encrypted, err := encryptSecret(s.cfg.SecretKey, *credentialInput)
		if err != nil {
			httpx.Error(w, r, fmt.Errorf("加密模型凭据失败：%w", err))
			return
		}
		credentialCipher = encrypted
		authConfigured = true
		credentialChanged = true
	}
	configStatus := "incomplete"
	if endpoint != "" && model != "" {
		configStatus = "configured"
	}
	connectionStatus := row.ConnectionStatus
	capabilityStatus := row.CapabilityStatus
	if endpoint != row.Endpoint || model != row.Model || provider != row.Provider || credentialChanged {
		connectionStatus = "unknown"
	}
	if model != row.Model {
		capabilityStatus = "unverified"
	}
	capabilitiesRaw := row.Capabilities
	if request.Capabilities != nil {
		encoded, err := json.Marshal(request.Capabilities)
		if err != nil {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "invalid_capabilities", "能力配置不是有效的 JSON 对象。"))
			return
		}
		capabilitiesRaw = encoded
	}
	isDefault := row.IsDefault
	if request.IsDefault != nil {
		isDefault = *request.IsDefault
	}

	err = s.db.Tx(r.Context(), func(tx pgxTx) error {
		if isDefault {
			if _, err := tx.Exec(r.Context(), `
				update model_configs set is_default = false, updated_at = now()
				where kind = $1 and id <> $2::uuid and is_default`, row.Kind, id); err != nil {
				return err
			}
		}
		_, err := tx.Exec(r.Context(), `
			update model_configs set
				name = $2, provider = $3, endpoint = $4, model = $5,
				credential_ciphertext = $6, auth_configured = $7,
				config_status = $8, connection_status = $9, capability_status = $10,
				capabilities = $11::jsonb, is_default = $12, updated_at = now()
			where id = $1::uuid`,
			id, name, provider, endpoint, model, credentialCipher, authConfigured,
			configStatus, connectionStatus, capabilityStatus, string(capabilitiesRaw), isDefault)
		return err
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	updated, err := scanModelConfig(s.db.Pool.QueryRow(r.Context(), modelConfigSelect+` where id = $1::uuid`, id))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.record(r, "model_config.updated", "model_config", id, map[string]any{
		"kind": row.Kind, "configStatus": updated.ConfigStatus,
		"authConfigured": updated.AuthConfigured, "credentialChanged": credentialChanged,
		"isDefault": updated.IsDefault,
	})
	httpx.JSON(w, r, http.StatusOK, map[string]any{"config": updated.browserView()})
}

// ---------------------------------------------------------------- checks

type checkOutcome struct {
	Status     string    `json:"status"`
	Reason     string    `json:"reason,omitempty"`
	HTTPStatus int       `json:"httpStatus,omitempty"`
	LatencyMS  int64     `json:"latencyMs"`
	CheckedAt  time.Time `json:"checkedAt"`
	Detail     any       `json:"detail,omitempty"`
}

func (s *Service) handleCheckModelConfig(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	row, err := scanModelConfig(s.db.Pool.QueryRow(r.Context(), modelConfigSelect+` where id = $1::uuid`, id))
	if err != nil {
		httpx.Error(w, r, httpx.ErrNotFound)
		return
	}
	if strings.TrimSpace(row.Endpoint) == "" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "endpoint_missing", "该模型配置缺少 endpoint，无法执行连接检查。"))
		return
	}
	credential := ""
	credentialError := ""
	if row.CredentialCipher != "" {
		decrypted, err := decryptSecret(s.cfg.SecretKey, row.CredentialCipher)
		if err != nil {
			credentialError = "凭据无法解密（RKA_SECRET_KEY 可能已变更）：" + err.Error()
		} else {
			credential = decrypted
		}
	} else if row.AuthConfigured {
		credentialError = "配置标记为需要鉴权，但服务端没有可用凭据。"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	base := strings.TrimRight(row.Endpoint, "/")

	connection := checkConnection(ctx, base, credential)
	capability := checkOutcome{Status: "unverified", Reason: "连接失败，未执行能力验证。", CheckedAt: time.Now().UTC()}
	if credentialError != "" {
		connection = checkOutcome{Status: "failed", Reason: credentialError, CheckedAt: time.Now().UTC()}
	} else if connection.Status == "ok" || row.Kind == "llm" || row.Kind == "embedding" {
		capability = checkCapability(ctx, row.Kind, base, row.Model, credential)
		if capability.Status == "verified" && connection.Status != "ok" {
			connection = checkOutcome{Status: "ok", Reason: "模型接口可用（/models 不可用，但能力调用成功）", LatencyMS: capability.LatencyMS, CheckedAt: time.Now().UTC()}
		}
	}

	lastError := ""
	if connection.Status != "ok" {
		lastError = connection.Reason
	} else if capability.Status == "failed" {
		lastError = capability.Reason
	}
	capabilities := map[string]any{
		"lastCheck": map[string]any{
			"connectionStatus": connection.Status,
			"capabilityStatus": capability.Status,
			"checkedAt":        time.Now().UTC(),
		},
	}
	if detail, ok := connection.Detail.(map[string]any); ok {
		for key, value := range detail {
			capabilities[key] = value
		}
	}
	if detail, ok := capability.Detail.(map[string]any); ok {
		for key, value := range detail {
			capabilities[key] = value
		}
	}
	encoded, _ := json.Marshal(capabilities)
	if _, err := s.db.Pool.Exec(r.Context(), `
		update model_configs set
			connection_status = $2, capability_status = $3, capabilities = $4::jsonb,
			last_checked_at = now(), last_error = $5, updated_at = now()
		where id = $1::uuid`, id, connection.Status, capability.Status, string(encoded), lastError); err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.record(r, "model_config.checked", "model_config", id, map[string]any{
		"kind": row.Kind, "connectionStatus": connection.Status, "capabilityStatus": capability.Status,
	})
	httpx.JSON(w, r, http.StatusOK, map[string]any{
		"connection": connection,
		"capability": capability,
		"config": map[string]any{
			"id": row.ID, "kind": row.Kind, "name": row.Name,
			"configStatus": row.ConfigStatus, "connectionStatus": connection.Status,
			"capabilityStatus": capability.Status, "lastError": lastError,
		},
	})
}

func checkConnection(ctx context.Context, base, credential string) checkOutcome {
	start := time.Now()
	status, payload, err := doJSON(ctx, http.MethodGet, base+"/models", credential, nil)
	outcome := checkOutcome{CheckedAt: time.Now().UTC(), LatencyMS: time.Since(start).Milliseconds(), HTTPStatus: status}
	if err != nil {
		outcome.Status = "failed"
		outcome.Reason = "连接模型网关失败：" + err.Error()
		return outcome
	}
	if status < 200 || status >= 300 {
		outcome.Status = "failed"
		outcome.Reason = fmt.Sprintf("模型网关 /models 返回 HTTP %d：%s", status, truncateBody(payload, 300))
		return outcome
	}
	outcome.Status = "ok"
	models := []string{}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(payload, &parsed) == nil {
		for _, item := range parsed.Data {
			if item.ID != "" && len(models) < 100 {
				models = append(models, item.ID)
			}
		}
	}
	outcome.Detail = map[string]any{"models": models}
	return outcome
}

func checkCapability(ctx context.Context, kind, base, model, credential string) checkOutcome {
	start := time.Now()
	outcome := checkOutcome{CheckedAt: time.Now().UTC(), LatencyMS: 0}
	switch kind {
	case "llm":
		body := map[string]any{
			"model": model,
			"messages": []map[string]any{
				{"role": "user", "content": "ping"},
			},
			"max_tokens": 1,
			"stream":     false,
		}
		status, payload, err := doJSON(ctx, http.MethodPost, base+"/chat/completions", credential, body)
		outcome.LatencyMS = time.Since(start).Milliseconds()
		outcome.HTTPStatus = status
		if err != nil {
			outcome.Status = "failed"
			outcome.Reason = "能力验证请求失败：" + err.Error()
			return outcome
		}
		if status < 200 || status >= 300 {
			outcome.Status = "failed"
			outcome.Reason = fmt.Sprintf("chat/completions 返回 HTTP %d：%s", status, truncateBody(payload, 300))
			return outcome
		}
		var parsed struct {
			Choices []json.RawMessage `json:"choices"`
		}
		if json.Unmarshal(payload, &parsed) != nil || len(parsed.Choices) == 0 {
			outcome.Status = "failed"
			outcome.Reason = "chat/completions 响应缺少 choices，不能确认生成能力。"
			return outcome
		}
		outcome.Status = "verified"
		outcome.Detail = map[string]any{"chat": true}
		return outcome
	case "embedding":
		body := map[string]any{"model": model, "input": "ping"}
		status, payload, err := doJSON(ctx, http.MethodPost, base+"/embeddings", credential, body)
		outcome.LatencyMS = time.Since(start).Milliseconds()
		outcome.HTTPStatus = status
		if err != nil {
			outcome.Status = "failed"
			outcome.Reason = "Embedding 能力验证请求失败：" + err.Error()
			return outcome
		}
		if status < 200 || status >= 300 {
			outcome.Status = "failed"
			outcome.Reason = fmt.Sprintf("embeddings 返回 HTTP %d：%s", status, truncateBody(payload, 300))
			return outcome
		}
		var parsed struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}
		if json.Unmarshal(payload, &parsed) != nil || len(parsed.Data) == 0 || len(parsed.Data[0].Embedding) == 0 {
			outcome.Status = "failed"
			outcome.Reason = "embeddings 响应缺少向量，不能确认嵌入能力。"
			return outcome
		}
		outcome.Status = "verified"
		outcome.Detail = map[string]any{"embedding": true, "dimensions": len(parsed.Data[0].Embedding)}
		return outcome
	default:
		outcome.Status = "unverified"
		outcome.Reason = "暂不支持该模型类型的能力验证。"
		return outcome
	}
}

func doJSON(ctx context.Context, method, endpoint, credential string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	client := &http.Client{Timeout: 12 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	return response.StatusCode, payload, nil
}

func truncateBody(payload []byte, max int) string {
	value := strings.TrimSpace(string(payload))
	if len(value) > max {
		value = value[:max] + "…"
	}
	return value
}

// ---------------------------------------------------------------- crypto

// encryptSecret seals a credential with AES-256-GCM using cfg.SecretKey.
// Format: v1:<base64(nonce||ciphertext)>.
func encryptSecret(key []byte, plaintext string) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("RKA_SECRET_KEY 必须是 32 字节")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return "v1:" + base64.RawStdEncoding.EncodeToString(sealed), nil
}

func decryptSecret(key []byte, encoded string) (string, error) {
	if !strings.HasPrefix(encoded, "v1:") {
		return "", fmt.Errorf("不支持的凭据密文格式")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(encoded, "v1:"))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", fmt.Errorf("凭据密文长度不正确")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("凭据解密失败")
	}
	return string(plaintext), nil
}

type createModelConfigRequest struct {
	Name         string         `json:"name"`
	Kind         string         `json:"kind"`
	Provider     string         `json:"provider"`
	Endpoint     string         `json:"endpoint"`
	Model        string         `json:"model"`
	Credential   string         `json:"credential"`
	APIKey       string         `json:"apiKey"`
	IsDefault    bool           `json:"isDefault"`
	Capabilities map[string]any `json:"capabilities"`
}

// handleCreateModelConfig lets an administrator register a model without
// editing the deployment environment. Credentials are encrypted before storage
// and never returned.
func (s *Service) handleCreateModelConfig(w http.ResponseWriter, r *http.Request) {
	var request createModelConfigRequest
	if err := httpx.Decode(w, r, &request, 32<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	name := strings.TrimSpace(request.Name)
	if name == "" || len(name) > 120 {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "invalid_name", "配置名称不能为空且不能超过 120 个字符。"))
		return
	}
	kind := strings.TrimSpace(request.Kind)
	if kind == "" {
		kind = "llm"
	}
	if kind != "llm" && kind != "embedding" && kind != "rerank" {
		httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "invalid_kind", "模型类型只能是 llm、embedding 或 rerank。"))
		return
	}
	endpoint := strings.TrimSpace(request.Endpoint)
	if endpoint != "" {
		if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "invalid_endpoint", "endpoint 必须以 http:// 或 https:// 开头。"))
			return
		}
		if parsed, err := url.Parse(endpoint); err != nil || parsed.User != nil {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "invalid_endpoint", "endpoint 不能包含用户名或密码。"))
			return
		}
	}
	modelName := strings.TrimSpace(request.Model)
	provider := strings.TrimSpace(request.Provider)
	if provider == "" {
		provider = "openai-compatible"
	}
	credential := request.Credential
	if credential == "" {
		credential = request.APIKey
	}
	credentialCipher := ""
	authConfigured := false
	if strings.TrimSpace(credential) != "" {
		encrypted, err := encryptSecret(s.cfg.SecretKey, credential)
		if err != nil {
			httpx.Error(w, r, fmt.Errorf("加密模型凭据失败：%w", err))
			return
		}
		credentialCipher = encrypted
		authConfigured = true
	}
	configStatus := "incomplete"
	if endpoint != "" && modelName != "" {
		configStatus = "configured"
	}
	capabilities := []byte("{}")
	if request.Capabilities != nil {
		encoded, err := json.Marshal(request.Capabilities)
		if err != nil {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "invalid_capabilities", "能力配置不是有效的 JSON 对象。"))
			return
		}
		capabilities = encoded
	}
	var id string
	err := s.db.Tx(r.Context(), func(tx pgxTx) error {
		if request.IsDefault {
			if _, err := tx.Exec(r.Context(), `
				update model_configs set is_default = false, updated_at = now()
				where kind = $1 and is_default`, kind); err != nil {
				return err
			}
		}
		return tx.QueryRow(r.Context(), `
			insert into model_configs (name, kind, provider, endpoint, model, credential_ciphertext,
				auth_configured, config_status, capabilities, is_default, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11::uuid)
			returning id::text`,
			name, kind, provider, endpoint, modelName, credentialCipher, authConfigured,
			configStatus, string(capabilities), request.IsDefault, auth.MustUser(r.Context()).ID).Scan(&id)
	})
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	created, err := scanModelConfig(s.db.Pool.QueryRow(r.Context(), modelConfigSelect+` where id = $1::uuid`, id))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	s.record(r, "model_config.created", "model_config", id, map[string]any{"kind": kind, "configStatus": created.ConfigStatus})
	httpx.JSON(w, r, http.StatusCreated, map[string]any{"config": created.browserView()})
}

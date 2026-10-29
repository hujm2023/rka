package ops

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"rka/internal/store"
)

// ComponentHealth is one independently reported health signal. "unconfigured"
// is intentionally distinct from "failed": an optional dependency that was
// never configured is not an outage, but it must never be reported as ok.
type ComponentHealth struct {
	Status  string         `json:"status"` // ok | degraded | failed | unconfigured
	Reason  string         `json:"reason,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

// VersionInfo records the versions an operator needs to reason about a running
// instance and about backup compatibility.
type VersionInfo struct {
	App          string `json:"app"`
	Schema       int64  `json:"schema"`
	BinarySchema int64  `json:"binarySchema"`
	Content      string `json:"contentVersion"`
	Bank         string `json:"bankVersion"`
	Go           string `json:"go"`
	PostgreSQL   string `json:"postgres,omitempty"`
}

// HealthReport separates process, database, teaching, model and index health.
// There is no single boolean that can hide an unconfigured or failing
// dependency.
type HealthReport struct {
	Status    string          `json:"status"` // ok | degraded | unavailable
	Process   ComponentHealth `json:"process"`
	Database  ComponentHealth `json:"database"`
	Teaching  ComponentHealth `json:"teaching"`
	Model     ComponentHealth `json:"model"`
	Index     ComponentHealth `json:"index"`
	Version   VersionInfo     `json:"version"`
	CheckedAt time.Time       `json:"checkedAt"`
}

// Health collects every health signal with an independent timeout so one slow
// dependency cannot block the admin status page indefinitely.
func (s *Service) Health(ctx context.Context) HealthReport {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	report := HealthReport{CheckedAt: time.Now().UTC()}
	report.Process = s.processHealth()
	report.Database = s.databaseHealth(ctx)
	report.Teaching = s.teachingHealth(ctx)
	report.Model = s.modelHealth(ctx)
	report.Index = s.indexHealth(ctx)
	report.Version = s.versionInfo(ctx)

	switch {
	case report.Database.Status == "failed" || report.Process.Status == "failed":
		report.Status = "unavailable"
	case report.Database.Status == "degraded" || report.Teaching.Status == "failed" ||
		report.Model.Status == "failed" || report.Index.Status == "failed":
		report.Status = "degraded"
	default:
		report.Status = "ok"
	}
	return report
}

func (s *Service) processHealth() ComponentHealth {
	return ComponentHealth{
		Status: "ok",
		Details: map[string]any{
			"mode":          s.cfg.Mode,
			"addr":          s.cfg.Addr,
			"dataDir":       s.cfg.DataDir,
			"goVersion":     runtime.Version(),
			"uptimeSeconds": int64(time.Since(s.startedAt).Seconds()),
			"pid":           os.Getpid(),
		},
	}
}

func (s *Service) databaseHealth(ctx context.Context) ComponentHealth {
	if err := s.db.Healthy(ctx); err != nil {
		return ComponentHealth{Status: "failed", Reason: "数据库连接失败：" + err.Error()}
	}
	details := map[string]any{}
	binaryVersion, binErr := store.BinarySchemaVersion()
	currentVersion, versionErr := s.db.SchemaVersion(ctx)
	status := "ok"
	reason := ""
	switch {
	case binErr != nil:
		status, reason = "degraded", "无法读取二进制内置 schema 版本："+binErr.Error()
	case versionErr != nil:
		status, reason = "failed", "无法读取数据库 schema 版本："+versionErr.Error()
	case binaryVersion != currentVersion:
		status = "degraded"
		reason = fmt.Sprintf("数据库 schema 版本 %d 与二进制版本 %d 不一致，需要执行 rka migrate 或回退镜像。", currentVersion, binaryVersion)
	default:
		details["schemaVersion"] = currentVersion
	}
	if binErr == nil {
		details["binarySchemaVersion"] = binaryVersion
	}

	var materials, questions int
	if err := s.db.Pool.QueryRow(ctx, `select count(*) from materials where review_status = 'published'`).Scan(&materials); err != nil {
		status, reason = "failed", "读取资料数量失败："+err.Error()
	} else {
		details["publishedMaterials"] = materials
	}
	if err := s.db.Pool.QueryRow(ctx, `select count(*) from questions where review_status = 'published'`).Scan(&questions); err != nil {
		status, reason = "failed", "读取题目数量失败："+err.Error()
	} else {
		details["publishedQuestions"] = questions
	}
	contentReady := materials > 0 && questions > 0
	details["content"] = map[string]any{
		"ready": contentReady, "publishedMaterials": materials, "publishedQuestions": questions,
	}
	if !contentReady {
		details["contentReason"] = "核心内容为空（已发布资料或题目为 0），请执行 rka import-content；数据库连接本身正常。"
	}

	var pgVersion string
	if err := s.db.Pool.QueryRow(ctx, `select current_setting('server_version')`).Scan(&pgVersion); err == nil {
		details["postgresVersion"] = pgVersion
	}
	return ComponentHealth{Status: status, Reason: reason, Details: details}
}

func (s *Service) teachingHealth(ctx context.Context) ComponentHealth {
	details := map[string]any{
		"embedding": embeddingStatus(s.cfg.Teaching.EmbeddingURL, s.cfg.Teaching.EmbeddingModel, s.cfg.Teaching.EmbeddingDims),
	}
	if strings.TrimSpace(s.cfg.Teaching.DeepTutorURL) == "" {
		details["configured"] = false
		return ComponentHealth{
			Status:  "unconfigured",
			Reason:  "未配置 DeepTutor 教学服务（RKA_DEEPTUTOR_URL 为空）。AI 教学任务不可用，登录、阅读与练习不受影响。",
			Details: details,
		}
	}
	details["configured"] = true
	details["baseUrl"] = s.cfg.Teaching.DeepTutorURL
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	status, latency, err := probeHTTP(probeCtx, s.cfg.Teaching.DeepTutorURL, s.cfg.Teaching.DeepTutorToken)
	details["latencyMs"] = latency.Milliseconds()
	if err != nil {
		details["reachable"] = false
		return ComponentHealth{Status: "failed", Reason: "教学服务不可达：" + err.Error(), Details: details}
	}
	details["reachable"] = true
	details["httpStatus"] = status
	switch {
	case status >= 500:
		return ComponentHealth{Status: "failed", Reason: fmt.Sprintf("教学服务返回 HTTP %d。", status), Details: details}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ComponentHealth{Status: "failed", Reason: fmt.Sprintf("教学服务鉴权失败（HTTP %d），请检查 RKA_DEEPTUTOR_TOKEN。", status), Details: details}
	case status == http.StatusNotFound:
		return ComponentHealth{Status: "degraded", Reason: "教学服务可达，但健康检查返回 404，无法确认健康状态。", Details: details}
	default:
		return ComponentHealth{Status: "ok", Details: details}
	}
}

func (s *Service) modelHealth(ctx context.Context) ComponentHealth {
	type configRow struct {
		ID, Name, Kind, Provider, Endpoint, Model string
		AuthConfigured                            bool
		ConfigStatus, ConnectionStatus, CapStatus string
		LastCheckedAt                             *time.Time
		LastError                                 string
		IsDefault                                 bool
	}
	rows, err := s.db.Pool.Query(ctx, `
		select id::text, name, kind, provider, endpoint, model, auth_configured,
		       config_status, connection_status, capability_status, last_checked_at,
		       last_error, is_default
		from model_configs order by kind, is_default desc, created_at`)
	if err != nil {
		return ComponentHealth{Status: "failed", Reason: "读取模型配置失败：" + err.Error()}
	}
	defer rows.Close()

	summaries := make([]map[string]any, 0, 4)
	configured := 0
	checkedOK := 0
	hasFailure := false
	var failureReason string
	for rows.Next() {
		var row configRow
		if err := rows.Scan(&row.ID, &row.Name, &row.Kind, &row.Provider, &row.Endpoint, &row.Model,
			&row.AuthConfigured, &row.ConfigStatus, &row.ConnectionStatus, &row.CapStatus,
			&row.LastCheckedAt, &row.LastError, &row.IsDefault); err != nil {
			return ComponentHealth{Status: "failed", Reason: "读取模型配置失败：" + err.Error()}
		}
		summary := map[string]any{
			"id": row.ID, "name": row.Name, "kind": row.Kind, "provider": row.Provider,
			"endpoint": row.Endpoint, "model": row.Model, "authConfigured": row.AuthConfigured,
			"configStatus": row.ConfigStatus, "connectionStatus": row.ConnectionStatus,
			"capabilityStatus": row.CapStatus, "lastCheckedAt": row.LastCheckedAt,
			"lastError": row.LastError, "isDefault": row.IsDefault,
		}
		summaries = append(summaries, summary)
		if row.ConfigStatus != "configured" {
			continue
		}
		configured++
		if row.ConnectionStatus == "failed" {
			hasFailure = true
			failureReason = row.LastError
			if failureReason == "" {
				failureReason = "连接检查失败但未记录原因。"
			}
		}
		if row.ConnectionStatus == "ok" && row.CapStatus != "failed" {
			checkedOK++
		}
	}

	envConfigured := strings.TrimSpace(s.cfg.AI.Endpoint) != ""
	details := map[string]any{
		"configs": summaries,
		"environment": map[string]any{
			"configured":     envConfigured,
			"endpoint":       s.cfg.AI.Endpoint,
			"model":          s.cfg.AI.Model,
			"authConfigured": strings.TrimSpace(s.cfg.AI.APIKey) != "",
			"gatewayName":    s.cfg.AI.GatewayName,
		},
	}
	switch {
	case hasFailure:
		return ComponentHealth{Status: "failed", Reason: "模型连接检查失败：" + failureReason, Details: details}
	case configured == 0 && !envConfigured:
		details["configured"] = false
		return ComponentHealth{
			Status:  "unconfigured",
			Reason:  "未配置模型网关（无 model_configs 记录且 RKA_AI_ENDPOINT 为空）。AI 功能不可用，基础学习功能不受影响。",
			Details: details,
		}
	case configured == 0 && envConfigured:
		details["configured"] = true
		return ComponentHealth{
			Status:  "degraded",
			Reason:  "模型通过环境变量配置，但尚未执行连接检查，能力状态未知。",
			Details: details,
		}
	case checkedOK == configured:
		details["configured"] = true
		return ComponentHealth{Status: "ok", Details: details}
	default:
		details["configured"] = true
		return ComponentHealth{
			Status:  "degraded",
			Reason:  "模型已配置，但连接或能力状态尚未验证，请在管理后台执行连接检查。",
			Details: details,
		}
	}
}

func (s *Service) indexHealth(ctx context.Context) ComponentHealth {
	details := map[string]any{
		"embedding": embeddingStatus(s.cfg.Teaching.EmbeddingURL, s.cfg.Teaching.EmbeddingModel, s.cfg.Teaching.EmbeddingDims),
	}
	if info, err := os.Stat(s.cfg.DerivedDir()); err != nil {
		details["derivedDir"] = map[string]any{"path": s.cfg.DerivedDir(), "exists": false}
		if os.IsNotExist(err) {
			// A missing derived directory is recoverable by initialisation, not
			// an index failure by itself; keep it visible in details.
			details["derivedDirNote"] = "衍生目录不存在，执行 rka init 可重建。"
		}
	} else if info.IsDir() {
		details["derivedDir"] = map[string]any{"path": s.cfg.DerivedDir(), "exists": true}
	}

	rows, err := s.db.Pool.Query(ctx, `select index_status, count(*) from material_versions group by index_status`)
	if err != nil {
		return ComponentHealth{Status: "failed", Reason: "读取索引状态失败：" + err.Error(), Details: details}
	}
	defer rows.Close()
	counts := map[string]int{}
	total := 0
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return ComponentHealth{Status: "failed", Reason: "读取索引状态失败：" + err.Error(), Details: details}
		}
		counts[status] = count
		total += count
	}
	details["counts"] = counts
	details["totalVersions"] = total

	var contentVersion string
	_ = s.db.Pool.QueryRow(ctx, `select content_version from platform_state where id = 1`).Scan(&contentVersion)
	details["contentVersion"] = contentVersion

	switch {
	case total == 0:
		return ComponentHealth{Status: "unconfigured", Reason: "尚无索引构建记录（内容尚未导入或索引未构建）。", Details: details}
	case counts["failed"] > 0:
		return ComponentHealth{Status: "degraded", Reason: fmt.Sprintf("%d 个资料版本的索引构建失败。", counts["failed"]), Details: details}
	case counts["pending"]+counts["building"] > 0:
		return ComponentHealth{Status: "degraded", Reason: fmt.Sprintf("%d 个资料版本的索引仍在构建中。", counts["pending"]+counts["building"]), Details: details}
	default:
		return ComponentHealth{Status: "ok", Details: details}
	}
}

func (s *Service) versionInfo(ctx context.Context) VersionInfo {
	info := VersionInfo{
		App: appVersion(),
		Go:  runtime.Version(),
	}
	if version, err := store.BinarySchemaVersion(); err == nil {
		info.BinarySchema = version
	}
	if version, err := s.db.SchemaVersion(ctx); err == nil {
		info.Schema = version
	}
	_ = s.db.Pool.QueryRow(ctx, `select content_version, bank_version from platform_state where id = 1`).
		Scan(&info.Content, &info.Bank)
	_ = s.db.Pool.QueryRow(ctx, `select current_setting('server_version')`).Scan(&info.PostgreSQL)
	return info
}

func embeddingStatus(url, model string, dims int) map[string]any {
	configured := strings.TrimSpace(url) != ""
	status := "unconfigured"
	if configured {
		status = "configured"
	}
	return map[string]any{
		"status": status, "configured": configured,
		"endpoint": url, "model": model, "dims": dims,
		"reason": reasonIf(!configured, "未配置 Embedding 服务（RKA_EMBEDDING_URL 为空）；语义检索不可用，关键词检索不受影响。"),
	}
}

func reasonIf(condition bool, reason string) string {
	if condition {
		return reason
	}
	return ""
}

func appVersion() string {
	if value := strings.TrimSpace(os.Getenv("RKA_VERSION")); value != "" {
		return value
	}
	return "dev"
}

// probeHTTP performs a short reachability probe without leaking the token.
func probeHTTP(ctx context.Context, baseURL, token string) (int, time.Duration, error) {
	start := time.Now()
	client := &http.Client{Timeout: 3 * time.Second}
	endpoint := strings.TrimRight(baseURL, "/") + "/health/ready"
	if token != "" {
		endpoint = strings.TrimRight(baseURL, "/") + "/healthz"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, time.Since(start), err
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, time.Since(start), err
	}
	defer response.Body.Close()
	return response.StatusCode, time.Since(start), nil
}

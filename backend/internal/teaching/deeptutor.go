package teaching

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DeepTutor HTTP endpoints. The real contract of the pinned DeepTutor version
// is not verified yet, so every path lives here as a package constant and the
// payload mapper accepts both snake_case and camelCase responses. When the
// verified contract lands, only this file and these constants need to change.
const (
	dtHealthPath       = "/healthz"
	dtUsersPath        = "/api/users"
	dtTurnsPath        = "/api/turns"
	dtTurnPath         = "/api/turns/%s"
	dtTurnEventsPath   = "/api/turns/%s/events"
	dtTurnMessagesPath = "/api/turns/%s/messages"
	dtTurnCancelPath   = "/api/turns/%s/cancel"
)

type deepTutorBackend struct {
	baseURL string
	token   string
	client  *http.Client
	log     *slog.Logger

	mu          sync.Mutex
	provisioned map[string]bool
}

func newDeepTutorBackend(baseURL, token string, timeout time.Duration, log *slog.Logger) *deepTutorBackend {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &deepTutorBackend{
		baseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		token:       strings.TrimSpace(token),
		client:      &http.Client{Timeout: timeout},
		log:         log,
		provisioned: map[string]bool{},
	}
}

func (d *deepTutorBackend) Name() string { return "deeptutor" }

func (d *deepTutorBackend) Configured() bool { return d != nil && d.baseURL != "" }

// Health exposes the legacy health probe through the deeptutorBackend contract.
func (d *deepTutorBackend) Health(ctx context.Context) (deepTutorHealth, error) {
	return d.health(ctx)
}

// SafeBaseURL exposes the credential-free base URL.
func (d *deepTutorBackend) SafeBaseURL() string {
	return d.safeBaseURL()
}

func (d *deepTutorBackend) safeBaseURL() string {
	parsed, err := url.Parse(d.baseURL)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

type deepTutorHealth struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

func (d *deepTutorBackend) health(ctx context.Context) (deepTutorHealth, error) {
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response, err := d.request(callCtx, http.MethodGet, dtHealthPath, nil)
	if err != nil {
		return deepTutorHealth{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return deepTutorHealth{}, fmt.Errorf("健康检查返回 %d", response.StatusCode)
	}
	var info deepTutorHealth
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&info); err != nil {
		// A reachable service with a non-JSON health body is still reachable.
		return deepTutorHealth{}, nil
	}
	return info, nil
}

// provision creates the upstream teaching user once per identity. The endpoint
// is part of the unverified DeepTutor contract: if the pinned version does not
// expose it (404/405) RKA keeps its local mapping and continues, because the
// authoritative identity mapping is owned by RKA.
func (d *deepTutorBackend) provision(ctx context.Context, identity Identity) error {
	key := identity.TeachingUserID
	d.mu.Lock()
	already := d.provisioned[key]
	d.mu.Unlock()
	if already {
		return nil
	}
	payload := map[string]any{
		"user_id":     identity.TeachingUserID,
		"external_id": identity.UserID,
		"credential":  identity.Credential,
	}
	response, err := d.request(ctx, http.MethodPost, dtUsersPath, payload)
	if err != nil {
		if IsRetryable(err) || IsUnknownState(err) {
			return err
		}
		d.log.Warn("deeptutor user provisioning failed; continuing with local identity mapping", "error", err)
		d.markProvisioned(key)
		return nil
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		d.markProvisioned(key)
		return nil
	case response.StatusCode == http.StatusConflict:
		d.markProvisioned(key)
		return nil
	case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed:
		d.log.Warn("deeptutor user provisioning endpoint unavailable; assuming user ids are accepted by turn API")
		d.markProvisioned(key)
		return nil
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return &UpstreamError{Code: "teaching_auth_failed", Message: "DeepTutor 教学服务鉴权失败，请检查 RKA_DEEPTUTOR_TOKEN。", Status: response.StatusCode}
	default:
		return &UpstreamError{Code: "teaching_provision_failed", Status: response.StatusCode, Retryable: response.StatusCode >= 500}
	}
}

func (d *deepTutorBackend) markProvisioned(key string) {
	d.mu.Lock()
	d.provisioned[key] = true
	d.mu.Unlock()
}

func (d *deepTutorBackend) Start(ctx context.Context, req TurnRequest) (TurnRef, error) {
	if !d.Configured() {
		return TurnRef{}, ErrUnavailable
	}
	if err := d.provision(ctx, req.Identity); err != nil {
		return TurnRef{}, err
	}
	payload := map[string]any{
		"user_id": req.Identity.TeachingUserID,
		"kind":    req.Kind,
		"input":   req.Prompt,
		"hint":    req.Hint,
		"context": req.Context,
		"sources": req.Sources,
		"history": req.History,
	}
	response, err := d.request(ctx, http.MethodPost, dtTurnsPath, payload)
	if err != nil {
		return TurnRef{}, err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return TurnRef{}, d.httpError(response.StatusCode, body)
	}
	var decoded struct {
		TurnID     string `json:"turnId"`
		TurnIDAlt  string `json:"turn_id"`
		SessionID  string `json:"sessionId"`
		SessionAlt string `json:"session_id"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return TurnRef{}, &UpstreamError{Code: "teaching_protocol", Message: "DeepTutor 创建回合的响应不是有效 JSON。", Underlying: err}
	}
	turnID := firstNonEmpty(decoded.TurnID, decoded.TurnIDAlt)
	if turnID == "" {
		return TurnRef{}, &UpstreamError{Code: "teaching_protocol", Message: "DeepTutor 创建回合未返回 turn id。"}
	}
	return TurnRef{
		Identity:  req.Identity,
		TaskID:    req.TaskID,
		TurnID:    turnID,
		SessionID: firstNonEmpty(decoded.SessionID, decoded.SessionAlt),
	}, nil
}

func (d *deepTutorBackend) Stream(ctx context.Context, req TurnRequest, ref TurnRef, emit func(Event) error) (Result, error) {
	if !d.Configured() {
		return Result{}, ErrUnavailable
	}
	path := fmt.Sprintf(dtTurnEventsPath, url.PathEscape(ref.TurnID))
	response, err := d.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		return Result{}, d.httpError(response.StatusCode, body)
	}
	result := Result{TurnID: ref.TurnID, SessionID: ref.SessionID}
	var sawTerminal bool
	streamErr := readSSE(response.Body, func(frame sseFrame) error {
		event, ok, err := normalizeDeepTutorFrame(frame)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		switch event.Type {
		case "delta":
			if text, _ := event.Payload["text"].(string); text != "" {
				result.FinalText += text
			}
		case "question":
			result.Waiting = true
		case "done":
			sawTerminal = true
		case "error":
			sawTerminal = true
			code, _ := event.Payload["code"].(string)
			message, _ := event.Payload["message"].(string)
			return &UpstreamError{Code: firstNonEmpty(code, "teaching_upstream_error"), Message: message}
		case "cancelled":
			sawTerminal = true
			return ErrCancelled
		}
		return emit(event)
	})
	if streamErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			if errors.Is(ctxErr, context.Canceled) {
				return result, ErrCancelled
			}
			return result, &UpstreamError{Code: "teaching_timeout", Message: "DeepTutor 事件流超时，无法确认回合状态。", Unknown: true, Underlying: ctxErr}
		}
		var upstream *UpstreamError
		if errors.As(streamErr, &upstream) {
			return result, streamErr
		}
		if errors.Is(streamErr, ErrCancelled) {
			return result, streamErr
		}
		return result, &UpstreamError{Code: "teaching_stream_broken", Message: "DeepTutor 事件流中断，无法确认回合状态。", Unknown: true, Underlying: streamErr}
	}
	if !sawTerminal && !result.Waiting {
		return result, &UpstreamError{Code: "teaching_stream_incomplete", Message: "DeepTutor 事件流提前结束，无法确认回合状态。", Unknown: true}
	}
	return result, nil
}

func (d *deepTutorBackend) Reply(ctx context.Context, ref TurnRef, text string) error {
	path := fmt.Sprintf(dtTurnMessagesPath, url.PathEscape(ref.TurnID))
	response, err := d.request(ctx, http.MethodPost, path, map[string]any{"content": text})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return ErrReplyUnsupported
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return d.httpError(response.StatusCode, body)
	}
	return nil
}

func (d *deepTutorBackend) Cancel(ctx context.Context, ref TurnRef) error {
	path := fmt.Sprintf(dtTurnCancelPath, url.PathEscape(ref.TurnID))
	response, err := d.request(ctx, http.MethodPost, path, map[string]any{})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	if response.StatusCode == http.StatusNotFound {
		return nil // the turn is already gone; cancellation target reached
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return d.httpError(response.StatusCode, nil)
	}
	return nil
}

func (d *deepTutorBackend) Recover(ctx context.Context, ref TurnRef) (Recovery, error) {
	if ref.TurnID == "" {
		return Recovery{State: RecoveryNotFound, Reason: "RKA 尚未记录上游回合，重试是安全的。"}, nil
	}
	path := fmt.Sprintf(dtTurnPath, url.PathEscape(ref.TurnID))
	response, err := d.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return Recovery{State: RecoveryUnknown, Reason: err.Error()}, nil
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode == http.StatusNotFound {
		return Recovery{State: RecoveryNotFound, Reason: "DeepTutor 不存在该回合，可以安全重试。"}, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Recovery{State: RecoveryUnknown, Reason: fmt.Sprintf("查询回合返回 %d", response.StatusCode)}, nil
	}
	var decoded struct {
		Status     string `json:"status"`
		Output     string `json:"output"`
		Text       string `json:"text"`
		Message    string `json:"message"`
		Error      string `json:"error"`
		SessionID  string `json:"sessionId"`
		SessionAlt string `json:"session_id"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Recovery{State: RecoveryUnknown, Reason: "回合查询响应不是有效 JSON。"}, nil
	}
	switch strings.ToLower(decoded.Status) {
	case "completed", "done", "finished", "succeeded":
		return Recovery{State: RecoveryCompleted, Result: &Result{
			TurnID:    ref.TurnID,
			SessionID: firstNonEmpty(decoded.SessionID, decoded.SessionAlt),
			FinalText: firstNonEmpty(decoded.Output, decoded.Text),
		}}, nil
	case "running", "queued", "pending", "in_progress":
		return Recovery{State: RecoveryRunning, Reason: "上游回合仍在执行。"}, nil
	case "failed", "error", "cancelled", "canceled":
		return Recovery{State: RecoveryFailed, Reason: firstNonEmpty(decoded.Error, decoded.Message, "上游回合已失败。")}, nil
	default:
		return Recovery{State: RecoveryUnknown, Reason: "上游回合状态未知：" + decoded.Status}, nil
	}
}

// ---------------------------------------------------------------- transport

func (d *deepTutorBackend) request(ctx context.Context, method, path string, payload any) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, d.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json, text/event-stream")
	if d.token != "" {
		request.Header.Set("Authorization", "Bearer "+d.token)
	}
	response, err := d.client.Do(request)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	return response, nil
}

// httpError maps an upstream HTTP status to a retry decision. 4xx errors mean
// the request was rejected (permanent); 5xx/429 can be retried. Authentication
// failures are surfaced explicitly because retrying them is pointless.
func (d *deepTutorBackend) httpError(status int, body []byte) error {
	message := strings.TrimSpace(string(body))
	if len(message) > 300 {
		message = message[:300]
	}
	upstream := &UpstreamError{Status: status}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		upstream.Code = "teaching_auth_failed"
		upstream.Message = "DeepTutor 教学服务鉴权失败，请检查 RKA_DEEPTUTOR_TOKEN。"
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
		upstream.Code = "teaching_contract_mismatch"
		upstream.Message = fmt.Sprintf("DeepTutor 接口与适配器不一致（HTTP %d），请核对固定版本的接口合同。", status)
	case status == http.StatusTooManyRequests:
		upstream.Code = "teaching_rate_limited"
		upstream.Message = "DeepTutor 教学服务限流，请稍后重试。"
		upstream.Retryable = true
		upstream.Transient = true
	case status >= 500:
		upstream.Code = "teaching_upstream_error"
		upstream.Message = fmt.Sprintf("DeepTutor 教学服务返回 %d。", status)
		upstream.Retryable = true
		upstream.Transient = true
	default:
		upstream.Code = "teaching_rejected"
		upstream.Message = fmt.Sprintf("DeepTutor 拒绝了教学请求（HTTP %d）。", status)
	}
	if message != "" && upstream.Message == "" {
		upstream.Message = message
	}
	return upstream
}

// normalizeDeepTutorFrame maps an upstream SSE frame to the RKA event contract.
func normalizeDeepTutorFrame(frame sseFrame) (Event, bool, error) {
	name := strings.TrimSpace(frame.Event)
	data := map[string]any{}
	if strings.TrimSpace(frame.Data) != "" {
		if err := json.Unmarshal([]byte(frame.Data), &data); err != nil {
			return Event{}, false, &UpstreamError{Code: "teaching_protocol", Message: "DeepTutor 事件不是有效 JSON。", Underlying: err}
		}
	}
	if name == "" {
		name, _ = data["type"].(string)
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "delta", "text", "message.delta":
		text := firstString(data, "text", "delta", "content")
		if text == "" {
			return Event{}, false, nil
		}
		return Event{Type: "delta", Payload: map[string]any{"text": text}}, true, nil
	case "citation", "reference", "source":
		payload := map[string]any{
			"sourceId":   firstString(data, "sourceId", "source_id", "id"),
			"materialId": firstString(data, "materialId", "material_id"),
			"sectionId":  firstString(data, "sectionId", "section_id"),
			"sectionKey": firstString(data, "sectionKey", "section_key"),
			"title":      firstString(data, "title"),
		}
		return Event{Type: "citation", Payload: payload}, true, nil
	case "question", "ask", "waiting_user":
		payload := map[string]any{"text": firstString(data, "text", "question", "content")}
		return Event{Type: "question", Payload: payload}, true, nil
	case "done", "completed", "finished":
		return Event{Type: "done", Payload: map[string]any{"status": "completed"}}, true, nil
	case "error", "failed":
		return Event{Type: "error", Payload: map[string]any{
			"code":    firstString(data, "code"),
			"message": firstString(data, "message", "error"),
		}}, true, nil
	case "cancelled", "canceled":
		return Event{Type: "cancelled", Payload: map[string]any{"reason": firstString(data, "reason", "message")}}, true, nil
	default:
		return Event{}, false, nil
	}
}

// classifyTransportError separates "the request never reached the upstream"
// from "the request may have been accepted but the answer is unknown".
func classifyTransportError(err error) error {
	if err == nil {
		return nil
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return &UpstreamError{Code: "teaching_timeout", Message: "连接 DeepTutor 超时，无法确认回合状态。", Unknown: true, Underlying: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &UpstreamError{Code: "teaching_timeout", Message: "连接 DeepTutor 超时，无法确认回合状态。", Unknown: true, Underlying: err}
	}
	if errors.Is(err, context.Canceled) {
		return ErrCancelled
	}
	// Connection refused / DNS failures happen before the request is delivered,
	// so a retry cannot duplicate an upstream turn.
	return &UpstreamError{Code: "teaching_unreachable", Message: "无法连接 DeepTutor 教学服务，请检查服务与网络配置。", Retryable: true, Transient: true, Underlying: err}
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

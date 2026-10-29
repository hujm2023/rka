package teaching

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DeepTutor 1.6/1.7 protocol endpoints. The pinned server exposes its API
// on port 8001 (not the Next.js 3782 frontend). Turns are created over
// WebSocket protocol v2.0; there is no REST turn-creation endpoint and no
// static API token.
const (
	dtReadyPath     = "/health/ready"
	dtLoginPath     = "/api/auth/login"
	dtAuthUsersPath = "/api/auth/users"
	dtWSPath        = "/ws"
)

type dtTokenEntry struct {
	token     string
	expiresAt time.Time
}

// deepTutorWSBackend speaks the measured DeepTutor 1.6/1.7 protocol: an admin
// logs in for a JWT, creates one standard user per RKA learner, and each turn is
// started/subscribed over /ws with protocol_version "2.0".
type deepTutorWSBackend struct {
	baseURL       string
	adminUsername string
	adminPassword string
	timeout       time.Duration
	client        *http.Client
	log           *slog.Logger

	mu     sync.Mutex
	tokens map[string]dtTokenEntry
	users  map[string]bool
}

func newDeepTutorWSBackend(baseURL, adminUsername, adminPassword string, timeout time.Duration, log *slog.Logger) *deepTutorWSBackend {
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &deepTutorWSBackend{
		baseURL:       strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		adminUsername: strings.TrimSpace(adminUsername),
		adminPassword: adminPassword,
		timeout:       timeout,
		client:        &http.Client{Timeout: timeout},
		log:           log,
		tokens:        map[string]dtTokenEntry{},
		users:         map[string]bool{},
	}
}

func (d *deepTutorWSBackend) Name() string { return "deeptutor" }

func (d *deepTutorWSBackend) Configured() bool { return d != nil && d.baseURL != "" }

func (d *deepTutorWSBackend) adminConfigured() bool {
	return strings.TrimSpace(d.adminUsername) != "" && d.adminPassword != ""
}

func (d *deepTutorWSBackend) SafeBaseURL() string {
	parsed, err := url.Parse(d.baseURL)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func (d *deepTutorWSBackend) Health(ctx context.Context) (deepTutorHealth, error) {
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	response, err := d.doJSON(callCtx, http.MethodGet, dtReadyPath, nil, "")
	if err != nil {
		return deepTutorHealth{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return deepTutorHealth{}, fmt.Errorf("健康检查返回 %d", response.StatusCode)
	}
	var info deepTutorHealth
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&info); err != nil {
		return deepTutorHealth{Status: "ready"}, nil
	}
	if info.Status == "" {
		info.Status = "ready"
	}
	return info, nil
}

// ---------------------------------------------------------------- auth

// login signs in and captures the dt_token JWT from the Set-Cookie header.
// DeepTutor returns the token only as a cookie, not in the response body.
func (d *deepTutorWSBackend) login(ctx context.Context, username, password string) (string, error) {
	response, err := d.doJSON(ctx, http.MethodPost, dtLoginPath, map[string]any{
		"username": username, "password": password,
	}, "")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return "", &UpstreamError{Code: "teaching_auth_failed", Message: "DeepTutor 账号登录失败，请检查 RKA_DEEPTUTOR_ADMIN_USERNAME / RKA_DEEPTUTOR_ADMIN_PASSWORD 或教学身份凭据。", Status: response.StatusCode}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", &UpstreamError{Code: "teaching_login_failed", Message: fmt.Sprintf("DeepTutor 登录返回 %d。", response.StatusCode), Status: response.StatusCode, Retryable: response.StatusCode >= 500}
	}
	token := ""
	for _, cookie := range response.Cookies() {
		if cookie.Name == "dt_token" && cookie.Value != "" {
			token = cookie.Value
			break
		}
	}
	if token == "" {
		var parsed struct {
			AccessToken string `json:"access_token"`
			Token       string `json:"token"`
		}
		_ = json.Unmarshal(body, &parsed)
		token = firstNonEmpty(parsed.AccessToken, parsed.Token)
	}
	if token == "" {
		return "", &UpstreamError{Code: "teaching_auth_failed", Message: "DeepTutor 登录成功但未返回 dt_token Cookie。"}
	}
	expiresAt := jwtExpiry(token)
	key := "user:" + username
	if username == d.adminUsername {
		key = "admin:" + username
	}
	d.cacheToken(key, token, expiresAt)
	return token, nil
}

func (d *deepTutorWSBackend) cacheToken(key, token string, expiresAt time.Time) {
	d.mu.Lock()
	d.tokens[key] = dtTokenEntry{token: token, expiresAt: expiresAt}
	d.mu.Unlock()
}

func (d *deepTutorWSBackend) cachedToken(key string) (string, bool) {
	d.mu.Lock()
	entry, ok := d.tokens[key]
	d.mu.Unlock()
	if !ok || entry.token == "" {
		return "", false
	}
	// Refresh one minute before expiry.
	if time.Until(entry.expiresAt) < time.Minute {
		return "", false
	}
	return entry.token, true
}

func (d *deepTutorWSBackend) invalidateToken(key string) {
	d.mu.Lock()
	delete(d.tokens, key)
	d.mu.Unlock()
}

func (d *deepTutorWSBackend) adminToken(ctx context.Context) (string, error) {
	if token, ok := d.cachedToken("admin:" + d.adminUsername); ok {
		return token, nil
	}
	if d.adminUsername == "" || d.adminPassword == "" {
		return "", &UpstreamError{Code: "teaching_admin_unconfigured", Message: "未配置 DeepTutor 管理员账号（RKA_DEEPTUTOR_ADMIN_USERNAME / RKA_DEEPTUTOR_ADMIN_PASSWORD），无法自动创建教学身份。"}
	}
	token, err := d.login(ctx, d.adminUsername, d.adminPassword)
	if err == nil {
		return token, nil
	}
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Status != http.StatusUnauthorized {
		return "", err
	}
	var status struct {
		First bool `json:"is_first_user"`
	}
	if probeErr := d.jsonCall(ctx, http.MethodGet, "/api/auth/is_first_user", nil, "", &status); probeErr != nil || !status.First {
		return "", err
	}
	// The native service enforces bootstrap-only registration; never reset an existing admin.
	if registerErr := d.jsonCall(ctx, http.MethodPost, "/api/auth/register", map[string]any{"username": d.adminUsername, "password": d.adminPassword}, "", nil); registerErr != nil {
		return "", registerErr
	}
	return d.login(ctx, d.adminUsername, d.adminPassword)
}

// ensureUser creates the private DeepTutor account for one RKA user. Creation
// is idempotent by username; an existing account is accepted and then logged in.
func (d *deepTutorWSBackend) ensureUser(ctx context.Context, identity Identity) error {
	key := "user:" + identity.TeachingUserID
	if _, ok := d.cachedToken(key); ok {
		return nil
	}
	d.mu.Lock()
	provisioned := d.users[identity.TeachingUserID]
	d.mu.Unlock()
	if !provisioned {
		admin, err := d.adminToken(ctx)
		if err != nil {
			return err
		}
		response, err := d.doJSON(ctx, http.MethodPost, dtAuthUsersPath, map[string]any{
			"username": identity.TeachingUserID,
			"password": identity.Credential,
			"preset":   "standard",
		}, admin)
		if err != nil {
			return err
		}
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		switch {
		case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
			d.invalidateToken("admin:" + d.adminUsername)
			return &UpstreamError{Code: "teaching_auth_failed", Message: "DeepTutor 管理员鉴权失败，无法创建教学账号。", Status: response.StatusCode}
		case response.StatusCode == http.StatusConflict:
			// Account already exists; the stored credential should still work.
		case response.StatusCode >= 200 && response.StatusCode < 300:
			// Created.
		case response.StatusCode >= 500:
			return &UpstreamError{Code: "teaching_user_create_failed", Message: fmt.Sprintf("DeepTutor 创建教学账号返回 %d。", response.StatusCode), Status: response.StatusCode, Retryable: true}
		default:
			message := strings.TrimSpace(string(body))
			if len(message) > 200 {
				message = message[:200]
			}
			return &UpstreamError{Code: "teaching_user_create_failed", Message: "DeepTutor 拒绝创建教学账号：" + message, Status: response.StatusCode}
		}
		d.mu.Lock()
		d.users[identity.TeachingUserID] = true
		d.mu.Unlock()
	}
	if _, err := d.login(ctx, identity.TeachingUserID, identity.Credential); err != nil {
		var upstream *UpstreamError
		if errors.As(err, &upstream) && upstream.Code == "teaching_auth_failed" {
			upstream.Message = "教学账号已存在但凭据不匹配（可能在 DeepTutor 侧被重置）。请管理员清理该教学账号后重建。"
		}
		return err
	}
	return nil
}

func (d *deepTutorWSBackend) userToken(ctx context.Context, identity Identity) (string, error) {
	key := "user:" + identity.TeachingUserID
	if token, ok := d.cachedToken(key); ok {
		return token, nil
	}
	if err := d.ensureUser(ctx, identity); err != nil {
		return "", err
	}
	if token, ok := d.cachedToken(key); ok {
		return token, nil
	}
	// ensureUser logged in and cached the token; this is only a safety net.
	return d.login(ctx, identity.TeachingUserID, identity.Credential)
}

// ---------------------------------------------------------------- turns

func (d *deepTutorWSBackend) Start(ctx context.Context, req TurnRequest) (TurnRef, error) {
	if !d.Configured() {
		return TurnRef{}, ErrUnavailable
	}
	token, err := d.userToken(ctx, req.Identity)
	if err != nil {
		return TurnRef{}, err
	}
	conn, err := d.openWS(ctx, token)
	if err != nil {
		return TurnRef{}, err
	}
	defer conn.Close()
	stop := conn.closeOnContext(ctx)
	defer stop()

	startCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	command := map[string]any{
		"type":                 "start_turn",
		"protocol_version":     "2.0",
		"content":              renderTurnContent(req),
		"capability":           "chat",
		"client_submission_id": firstNonEmpty(req.SubmissionID, req.TaskID),
		"language":             "zh-CN",
		"knowledge_bases":      req.KnowledgeBases,
		"tools":                []string{"rag", "kb_files"},
	}
	if req.KnowledgeBases == nil {
		command["knowledge_bases"] = []string{}
	}
	if err := conn.WriteText(mustJSON(command)); err != nil {
		return TurnRef{}, &UpstreamError{Code: "teaching_start_unknown", Message: "创建回合请求发送中断，无法确认是否已接受。", Unknown: true, Underlying: err}
	}
	for {
		frame, err := readDTFrame(conn)
		if err != nil {
			return TurnRef{}, classifyWSReadError(startCtx, err, "创建回合未收到会话确认，无法确认上游状态。")
		}
		switch frame.Type() {
		case "session":
			turnID := frame.string("turn_id")
			if turnID == "" {
				continue
			}
			return TurnRef{
				Identity:  req.Identity,
				TaskID:    req.TaskID,
				TurnID:    turnID,
				SessionID: frame.string("session_id"),
			}, nil
		case "error", "protocol_error":
			return TurnRef{}, frameError(frame)
		case "command_ack":
			if accepted, ok := frame.raw["accepted"].(bool); ok && !accepted {
				return TurnRef{}, frameError(frame)
			}
		}
	}
}

func (d *deepTutorWSBackend) Stream(ctx context.Context, req TurnRequest, ref TurnRef, emit func(Event) error) (Result, error) {
	token, err := d.userToken(ctx, ref.Identity)
	if err != nil {
		return Result{}, err
	}
	conn, err := d.openWS(ctx, token)
	if err != nil {
		return Result{}, err
	}
	defer conn.Close()
	stop := conn.closeOnContext(ctx)
	defer stop()
	result := Result{TurnID: ref.TurnID, SessionID: ref.SessionID, UpstreamSeq: ref.AfterSeq}
	seenCitations := map[string]bool{}
	if err := conn.WriteText(mustJSON(map[string]any{"type": "subscribe_turn", "protocol_version": "2.0", "turn_id": ref.TurnID, "after_seq": ref.AfterSeq})); err != nil {
		return result, classifyTransportError(err)
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(d.timeout))
		frame, err := readDTFrame(conn)
		if err != nil {
			return result, classifyWSReadError(ctx, err, "DeepTutor 事件流中断，无法确认回合状态。")
		}
		seq := frame.Seq()
		if seq > 0 && seq <= ref.AfterSeq {
			continue
		}
		events := []Event{}
		collect := func(event Event) error {
			if event.Type == "citation" {
				if id, _ := event.Payload["sourceId"].(string); id != "" {
					if seenCitations[id] {
						return nil
					}
					seenCitations[id] = true
				}
			}
			events = append(events, event)
			return nil
		}
		terminal, frameErr := consumeFrame(&result, frame, collect)
		if terminal && frameErr == nil {
			if err := emitCitations(result.FinalText, req.Sources, collect); err != nil {
				return result, err
			}
		}
		if req.CommitFrame != nil {
			err = req.CommitFrame(seq, events)
		} else {
			for _, event := range events {
				if err = emit(event); err != nil {
					break
				}
			}
		}
		if err != nil {
			return result, err
		}
		if seq > result.UpstreamSeq {
			result.UpstreamSeq = seq
		}
		if frameErr != nil {
			return result, frameErr
		}
		if terminal {
			return result, nil
		}
	}
}

func consumeFrame(result *Result, frame dtFrame, emit func(Event) error) (bool, error) {
	switch frame.Type() {
	case "session":
		if id := frame.string("turn_id"); id != "" {
			result.TurnID = id
		}
		if id := frame.string("session_id"); id != "" {
			result.SessionID = id
		}
	case "content", "result":
		text := frame.string("content")
		if text != "" && (frame.Type() == "content" || result.FinalText == "") {
			result.FinalText += text
			return false, emit(Event{Type: "delta", Payload: map[string]any{"text": text}})
		}
	case "sources":
		return false, emitSourceFrame(frame, emit)
	case "wait_for_input":
		result.Waiting = true
		text := firstNonEmpty(frame.string("content"), stringValue(frame.Metadata(), "question"))
		result.FinalText = strings.TrimSpace(result.FinalText + "\n" + text)
		return true, emit(Event{Type: "question", Payload: map[string]any{"text": text}})
	case "error", "protocol_error":
		return true, frameError(frame)
	case "done":
		status := stringValue(frame.Metadata(), "status")
		if status == "cancelled" || status == "canceled" {
			return true, ErrCancelled
		}
		if status == "failed" || status == "error" {
			return true, frameError(frame)
		}
		return true, nil
	case "command_ack":
		if accepted, ok := frame.raw["accepted"].(bool); ok && !accepted {
			return true, frameError(frame)
		}
	}
	return false, nil
}

func (d *deepTutorWSBackend) Reply(ctx context.Context, ref TurnRef, text string) error {
	return d.command(ctx, ref, map[string]any{
		"type": "submit_user_reply", "protocol_version": "2.0",
		"turn_id": ref.TurnID, "text": text, "command_id": newCommandID(),
	}, 10*time.Second)
}

func (d *deepTutorWSBackend) Cancel(ctx context.Context, ref TurnRef) error {
	if ref.TurnID == "" {
		return nil
	}
	return d.command(ctx, ref, map[string]any{
		"type": "cancel_turn", "protocol_version": "2.0",
		"turn_id": ref.TurnID, "command_id": newCommandID(),
	}, 5*time.Second)
}

// command sends one WS command and waits for its command_ack.
func (d *deepTutorWSBackend) command(ctx context.Context, ref TurnRef, command map[string]any, timeout time.Duration) error {
	token, err := d.userToken(ctx, ref.Identity)
	if err != nil {
		return err
	}
	conn, err := d.openWS(ctx, token)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := conn.closeOnContext(ctx)
	defer stop()
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	if err := conn.WriteText(mustJSON(command)); err != nil {
		return classifyTransportError(err)
	}
	for {
		frame, err := readDTFrame(conn)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(callCtx.Err(), context.DeadlineExceeded) {
				return &UpstreamError{Code: "teaching_command_timeout", Message: "DeepTutor 命令未确认，无法确认上游状态。", Unknown: true}
			}
			if callCtx.Err() != nil {
				return ErrCancelled
			}
			return classifyTransportError(err)
		}
		switch frame.Type() {
		case "command_ack":
			if accepted, ok := frame.raw["accepted"].(bool); ok && !accepted {
				code := stringValue(frame.Metadata(), "error_code")
				if code == "not_found" || code == "unknown_turn" {
					return nil
				}
				return frameError(frame)
			}
			return nil
		case "done":
			return nil
		case "error", "protocol_error":
			return frameError(frame)
		}
	}
}

func (d *deepTutorWSBackend) Recover(ctx context.Context, ref TurnRef) (Recovery, error) {
	token, err := d.userToken(ctx, ref.Identity)
	if err != nil {
		return Recovery{State: RecoveryUnknown, Reason: err.Error()}, nil
	}
	conn, err := d.openWS(ctx, token)
	if err != nil {
		return Recovery{State: RecoveryUnknown, Reason: err.Error()}, nil
	}
	defer conn.Close()
	callCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	stop := conn.closeOnContext(callCtx)
	defer stop()
	_ = conn.SetReadDeadline(time.Now().Add(12 * time.Second))

	if ref.TurnID == "" {
		if err := conn.WriteText(mustJSON(map[string]any{
			"type": "check_active_turn", "protocol_version": "2.0",
		})); err != nil {
			return Recovery{State: RecoveryUnknown, Reason: err.Error()}, nil
		}
		for {
			frame, err := readDTFrame(conn)
			if err != nil {
				return Recovery{State: RecoveryUnknown, Reason: "查询活动回合失败：" + err.Error()}, nil
			}
			switch frame.Type() {
			case "active_turn_info":
				if turnID := frame.string("turn_id"); turnID != "" {
					return Recovery{State: RecoveryRunning, Reason: "上游存在活动回合。"}, nil
				}
				return Recovery{State: RecoveryNotFound, Reason: "上游没有活动回合，可以安全重试。"}, nil
			case "command_ack", "pong":
				continue
			case "error", "protocol_error":
				return Recovery{State: RecoveryUnknown, Reason: frameError(frame).Error()}, nil
			}
		}
	}

	if err := conn.WriteText(mustJSON(map[string]any{
		"type": "subscribe_turn", "protocol_version": "2.0",
		"turn_id": ref.TurnID, "after_seq": 0,
	})); err != nil {
		return Recovery{State: RecoveryUnknown, Reason: err.Error()}, nil
	}
	result := Result{TurnID: ref.TurnID, SessionID: ref.SessionID}
	for {
		frame, err := readDTFrame(conn)
		if err != nil {
			return Recovery{State: RecoveryUnknown, Reason: "确认上游回合超时或中断：" + err.Error()}, nil
		}
		switch frame.Type() {
		case "session":
			if sessionID := frame.string("session_id"); sessionID != "" {
				result.SessionID = sessionID
			}
		case "content":
			result.FinalText += frame.string("content")
		case "result":
			if result.FinalText == "" {
				result.FinalText = frame.string("content")
			}
		case "wait_for_input":
			if frame.Seq() > 0 && frame.Seq() <= ref.AfterSeq {
				continue
			}
			result.Waiting = true
			result.UpstreamSeq = frame.Seq()
			if result.FinalText == "" {
				result.FinalText = firstNonEmpty(frame.string("content"), stringValue(frame.Metadata(), "question"))
			}
			return Recovery{State: RecoveryWaiting, Result: &result, Reason: "上游回合正在等待用户回答。"}, nil
		case "done":
			status := stringValue(frame.Metadata(), "status")
			switch status {
			case "", "completed", "succeeded":
				return Recovery{State: RecoveryCompleted, Result: &result}, nil
			case "cancelled", "canceled":
				return Recovery{State: RecoveryFailed, Reason: "上游回合已取消。"}, nil
			default:
				return Recovery{State: RecoveryFailed, Reason: "上游回合状态：" + status}, nil
			}
		case "error":
			return Recovery{State: RecoveryFailed, Reason: frameError(frame).Error()}, nil
		case "protocol_error":
			return Recovery{State: RecoveryNotFound, Reason: "上游不存在该回合，可以安全重试。"}, nil
		case "command_ack":
			if accepted, ok := frame.raw["accepted"].(bool); ok && !accepted {
				code := stringValue(frame.Metadata(), "error_code")
				if code == "not_found" || code == "unknown_turn" {
					return Recovery{State: RecoveryNotFound, Reason: "上游不存在该回合，可以安全重试。"}, nil
				}
				return Recovery{State: RecoveryUnknown, Reason: "上游命令被拒绝：" + code}, nil
			}
		}
	}
}

// ---------------------------------------------------------------- plumbing

func (d *deepTutorWSBackend) openWS(ctx context.Context, token string) (*wsConn, error) {
	target, err := wsURL(d.baseURL, "")
	if err != nil {
		return nil, err
	}
	return dialWebSocket(ctx, target, http.Header{"Cookie": []string{(&http.Cookie{Name: "dt_token", Value: token}).String()}})
}

func (d *deepTutorWSBackend) doJSON(ctx context.Context, method, path string, payload any, token string) (*http.Response, error) {
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
	request.Header.Set("Accept", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
		request.AddCookie(&http.Cookie{Name: "dt_token", Value: token})
	}
	response, err := d.client.Do(request)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	return response, nil
}

// ---------------------------------------------------------------- frame helpers

type dtFrame struct {
	raw map[string]any
}

func readDTFrame(conn *wsConn) (dtFrame, error) {
	data, err := conn.ReadMessage()
	if err != nil {
		return dtFrame{}, err
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return dtFrame{}, &UpstreamError{Code: "teaching_protocol", Message: "DeepTutor WebSocket 帧不是有效 JSON。", Underlying: err}
	}
	return dtFrame{raw: decoded}, nil
}

func (f dtFrame) Seq() int64 {
	value, _ := f.raw["seq"].(float64)
	if value == 0 {
		value, _ = f.Metadata()["seq"].(float64)
	}
	return int64(value)
}

func (f dtFrame) Type() string {
	value, _ := f.raw["type"].(string)
	return value
}

func (f dtFrame) string(key string) string {
	value, _ := f.raw[key].(string)
	return value
}

func (f dtFrame) Metadata() map[string]any {
	meta, _ := f.raw["metadata"].(map[string]any)
	if meta == nil {
		return map[string]any{}
	}
	return meta
}

// frameError maps a DeepTutor error frame onto the RKA retry contract.
func frameError(frame dtFrame) error {
	meta := frame.Metadata()
	code := firstNonEmpty(stringValue(meta, "error_code"), stringValue(meta, "code"), "teaching_upstream_error")
	message := firstNonEmpty(frame.string("content"), stringValue(meta, "message"))
	status := stringValue(meta, "status")
	retryable, _ := meta["retryable"].(bool)
	switch status {
	case "recovering":
		return &UpstreamError{Code: code, Message: firstNonEmpty(message, "上游正在恢复，无法确认回合状态。"), Unknown: true}
	case "cancelled", "canceled":
		return ErrCancelled
	case "failed", "error":
		return &UpstreamError{Code: code, Message: firstNonEmpty(message, "上游回合执行失败。"), Retryable: retryable}
	}
	return &UpstreamError{Code: code, Message: firstNonEmpty(message, "DeepTutor 返回错误。"), Retryable: retryable}
}

func classifyWSReadError(ctx context.Context, err error, message string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.Canceled) {
			return ErrCancelled
		}
		return &UpstreamError{Code: "teaching_timeout", Message: message, Unknown: true, Underlying: ctxErr}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &UpstreamError{Code: "teaching_timeout", Message: message, Unknown: true, Underlying: err}
	}
	if errors.Is(err, errWSClosed) {
		return &UpstreamError{Code: "teaching_stream_broken", Message: message, Unknown: true}
	}
	return &UpstreamError{Code: "teaching_stream_broken", Message: message, Unknown: true, Underlying: err}
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("teaching: encode websocket command: " + err.Error())
	}
	return encoded
}

func newCommandID() string {
	return "cmd-" + randomHex(8)
}

func jwtExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Now().Add(20 * time.Hour)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Now().Add(20 * time.Hour)
	}
	var claims struct {
		ExpiresAt int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.ExpiresAt <= 0 {
		return time.Now().Add(20 * time.Hour)
	}
	expiry := time.Unix(claims.ExpiresAt, 0)
	if time.Until(expiry) < time.Minute {
		return time.Now().Add(time.Minute)
	}
	return expiry
}

// emitSourceFrame forwards a DeepTutor sources event as citation events. The
// exact metadata shape is not verified against a live model stream, so both a
// list of source objects and a single object are accepted.
func emitSourceFrame(frame dtFrame, emit func(Event) error) error {
	meta := frame.Metadata()
	var items []map[string]any
	if list, ok := meta["sources"].([]any); ok {
		for _, item := range list {
			if object, ok := item.(map[string]any); ok {
				items = append(items, object)
			}
		}
	}
	if len(items) == 0 {
		if object, ok := meta["source"].(map[string]any); ok {
			items = append(items, object)
		}
	}
	if len(items) == 0 {
		if content := frame.string("content"); content != "" {
			items = append(items, map[string]any{"title": content})
		}
	}
	for _, item := range items {
		payload := map[string]any{
			"sourceId":   firstNonEmpty(stringValue(item, "sourceId"), stringValue(item, "source_id"), stringValue(item, "id")),
			"materialId": firstNonEmpty(stringValue(item, "materialId"), stringValue(item, "material_id")),
			"sectionId":  firstNonEmpty(stringValue(item, "sectionId"), stringValue(item, "section_id")),
			"sectionKey": firstNonEmpty(stringValue(item, "sectionKey"), stringValue(item, "section_key")),
			"title":      stringValue(item, "title"),
			"text":       firstNonEmpty(stringValue(item, "content"), stringValue(item, "text")),
			"kbName":     stringValue(item, "kb_name"),
			"sourcePath": stringValue(item, "source"),
		}
		if err := emit(Event{Type: "citation", Payload: payload}); err != nil {
			return err
		}
	}
	return nil
}

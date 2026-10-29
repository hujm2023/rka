package teaching

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"rka/internal/config"
)

// gatewayBackend executes teaching turns through any OpenAI-compatible chat
// completion endpoint (RKA_AI_ENDPOINT / RKA_AI_MODEL / RKA_AI_API_KEY). It is
// stateless: replies always start a new turn carrying the full history.
type gatewayBackend struct {
	cfg    config.AIConfig
	client *http.Client
	log    *slog.Logger
}

func newGatewayBackend(cfg config.AIConfig, log *slog.Logger) *gatewayBackend {
	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &gatewayBackend{
		cfg:    cfg,
		client: &http.Client{Timeout: timeout},
		log:    log,
	}
}

func (g *gatewayBackend) Name() string { return "gateway" }

func (g *gatewayBackend) Configured() bool {
	return g != nil && strings.TrimSpace(g.cfg.Endpoint) != "" && strings.TrimSpace(g.cfg.Model) != ""
}

func (g *gatewayBackend) Start(ctx context.Context, req TurnRequest) (TurnRef, error) {
	if !g.Configured() {
		return TurnRef{}, ErrUnavailable
	}
	return TurnRef{
		Identity: req.Identity,
		TaskID:   req.TaskID,
		TurnID:   "gw-" + randomHex(8),
	}, nil
}

// Reply is unsupported because the gateway keeps no server-side session; the
// caller starts a fresh turn with the full conversation instead.
func (g *gatewayBackend) Reply(ctx context.Context, ref TurnRef, text string) error {
	return ErrReplyUnsupported
}

func (g *gatewayBackend) Cancel(ctx context.Context, ref TurnRef) error {
	// The stream is aborted through the request context by the worker; there is
	// no remote session to cancel.
	return nil
}

func (g *gatewayBackend) Recover(ctx context.Context, ref TurnRef) (Recovery, error) {
	return Recovery{State: RecoveryUnknown, Reason: "OpenAI 兼容网关无状态，无法查询未完成回合的最终状态。"}, nil
}

func (g *gatewayBackend) Stream(ctx context.Context, req TurnRequest, ref TurnRef, emit func(Event) error) (Result, error) {
	if !g.Configured() {
		return Result{}, ErrUnavailable
	}
	result := Result{TurnID: ref.TurnID, SessionID: ""}
	payload := map[string]any{
		"model":       g.cfg.Model,
		"messages":    buildMessages(req),
		"stream":      true,
		"temperature": 0.2,
		"max_tokens":  2600,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return result, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, g.cfg.Endpoint, bytes.NewReader(encoded))
	if err != nil {
		return result, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream, application/json")
	if g.cfg.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+g.cfg.APIKey)
	}
	response, err := g.client.Do(request)
	if err != nil {
		return result, classifyGatewayTransportError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 64*1024))
		return result, gatewayHTTPError(response.StatusCode, body)
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "text/event-stream") {
		return g.consumeWholeResponse(ctx, response, ref, req, emit)
	}
	result.FinalText, err = g.consumeStream(ctx, response, emit)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(result.FinalText) == "" {
		return result, &UpstreamError{Code: "model_empty_response", Message: "AI 未返回文本。"}
	}
	if err := emitCitations(result.FinalText, req.Sources, emit); err != nil {
		return result, err
	}
	return result, nil
}

// consumeStream reads OpenAI chat-completion chunks and emits delta events.
func (g *gatewayBackend) consumeStream(ctx context.Context, response *http.Response, emit func(Event) error) (string, error) {
	var text strings.Builder
	sawDone := false
	streamErr := readSSE(response.Body, func(frame sseFrame) error {
		data := strings.TrimSpace(frame.Data)
		if data == "" {
			return nil
		}
		if data == "[DONE]" {
			sawDone = true
			return errStreamDone
		}
		var chunk struct {
			Error *struct {
				Message string `json:"message"`
				Code    any    `json:"code"`
			} `json:"error"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return &UpstreamError{Code: "model_protocol", Message: "AI 流式数据格式异常。", Underlying: err}
		}
		if chunk.Error != nil {
			return &UpstreamError{Code: "model_error", Message: firstNonEmpty(chunk.Error.Message, "模型服务返回错误。")}
		}
		for _, choice := range chunk.Choices {
			delta := firstNonEmpty(choice.Delta.Content, choice.Message.Content)
			if delta == "" {
				continue
			}
			text.WriteString(delta)
			if text.Len() > 40000 {
				return &UpstreamError{Code: "model_output_too_long", Message: "AI 输出过长，请分段提问。"}
			}
			if err := emit(Event{Type: "delta", Payload: map[string]any{"text": delta}}); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(streamErr, errStreamDone) {
		return text.String(), nil
	}
	if streamErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			if errors.Is(ctxErr, context.Canceled) {
				return text.String(), ErrCancelled
			}
			return text.String(), &UpstreamError{Code: "model_timeout", Message: "AI 流式响应超时，无法确认输出是否完整。", Unknown: true, Underlying: ctxErr}
		}
		var upstream *UpstreamError
		if errors.As(streamErr, &upstream) {
			return text.String(), streamErr
		}
		return text.String(), &UpstreamError{Code: "model_stream_broken", Message: "AI 流式响应中断，无法确认输出是否完整。", Unknown: true, Underlying: streamErr}
	}
	if !sawDone {
		return text.String(), &UpstreamError{Code: "model_stream_incomplete", Message: "AI 输出中断；已显示部分内容。", Unknown: true}
	}
	return text.String(), nil
}

// consumeWholeResponse handles servers that ignore stream=true and answer with
// a single JSON document.
func (g *gatewayBackend) consumeWholeResponse(ctx context.Context, response *http.Response, ref TurnRef, req TurnRequest, emit func(Event) error) (Result, error) {
	result := Result{TurnID: ref.TurnID}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return result, &UpstreamError{Code: "model_stream_broken", Message: "读取 AI 响应失败。", Unknown: true, Underlying: err}
	}
	var decoded struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return result, &UpstreamError{Code: "model_protocol", Message: "AI 响应不是有效 JSON。", Underlying: err}
	}
	if decoded.Error != nil {
		return result, &UpstreamError{Code: "model_error", Message: decoded.Error.Message}
	}
	for _, choice := range decoded.Choices {
		if choice.Message.Content != "" {
			result.FinalText += choice.Message.Content
		}
	}
	if strings.TrimSpace(result.FinalText) == "" {
		return result, &UpstreamError{Code: "model_empty_response", Message: "AI 未返回文本。"}
	}
	if err := emit(Event{Type: "delta", Payload: map[string]any{"text": result.FinalText}}); err != nil {
		return result, err
	}
	if err := emitCitations(result.FinalText, req.Sources, emit); err != nil {
		return result, err
	}
	return result, nil
}

var errStreamDone = errors.New("stream done")

var citationPattern = regexp.MustCompile(`\[S(\d+)\]`)

// emitCitations reports only source ids that were actually offered to the
// model. The model cannot invent a citation and have it persisted.
func emitCitations(text string, sources []Source, emit func(Event) error) error {
	if len(sources) == 0 {
		return nil
	}
	known := map[string]Source{}
	for _, source := range sources {
		known[source.ID] = source
	}
	seen := map[string]bool{}
	for _, match := range citationPattern.FindAllStringSubmatch(text, -1) {
		id := "S" + match[1]
		source, ok := known[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		if err := emit(Event{Type: "citation", Payload: map[string]any{
			"sourceId":          source.ID,
			"materialId":        source.MaterialID,
			"materialVersionId": source.MaterialVersionID,
			"sectionId":         source.SectionID,
			"sectionKey":        source.SectionKey,
			"title":             source.Title,
		}}); err != nil {
			return err
		}
	}
	return nil
}

func gatewayHTTPError(status int, body []byte) error {
	message := strings.TrimSpace(string(body))
	if len(message) > 300 {
		message = message[:300]
	}
	upstream := &UpstreamError{Status: status}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		upstream.Code = "model_auth_failed"
		upstream.Message = "AI 模型鉴权失败，请检查 RKA_AI_API_KEY。"
	case status == http.StatusNotFound:
		upstream.Code = "model_endpoint_not_found"
		upstream.Message = "AI 接口地址不存在，请检查 RKA_AI_ENDPOINT。"
	case status == http.StatusTooManyRequests:
		upstream.Code = "model_rate_limited"
		upstream.Message = "AI 模型服务限流，请稍后重试。"
		upstream.Retryable = true
		upstream.Transient = true
	case status >= 500:
		upstream.Code = "model_upstream_error"
		upstream.Message = fmt.Sprintf("AI 模型服务返回 %d，请检查服务端模型、额度与接口配置。", status)
		upstream.Retryable = true
		upstream.Transient = true
	default:
		upstream.Code = "model_rejected"
		upstream.Message = fmt.Sprintf("AI 模型服务拒绝了请求（HTTP %d）。", status)
	}
	if message != "" && upstream.Code == "model_rejected" {
		upstream.Message += " " + message
	}
	return upstream
}

func classifyGatewayTransportError(err error) error {
	if err == nil {
		return nil
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return &UpstreamError{Code: "model_timeout", Message: "连接 AI 模型超时，无法确认输出是否完整。", Unknown: true, Underlying: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &UpstreamError{Code: "model_timeout", Message: "连接 AI 模型超时，无法确认输出是否完整。", Unknown: true, Underlying: err}
	}
	if errors.Is(err, context.Canceled) {
		return ErrCancelled
	}
	var dial *net.OpError
	if errors.As(err, &dial) && dial.Op == "dial" {
		return &UpstreamError{Code: "model_unreachable", Message: "无法连接 AI 模型服务，请检查接口配置与网络。", Retryable: true, Transient: true, Underlying: err}
	}
	return &UpstreamError{Code: "model_connection_lost", Message: "请求连接中断，上游是否接受无法确认，不自动重复调用。", Unknown: true, Underlying: err}
}

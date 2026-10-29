// Package httpx contains the transport helpers shared by every domain package:
// JSON envelopes, error mapping, request identity, logging, SSE and static files.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIError is the only error shape returned to browsers.
// {"error":{"code":"conflict","message":"...","details":{...}}}
type APIError struct {
	Status  int
	Code    string
	Message string
	Details any
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

func NewError(status int, code, message string) *APIError {
	return &APIError{Status: status, Code: code, Message: message}
}

func (e *APIError) WithDetails(details any) *APIError {
	copy := *e
	copy.Details = details
	return &copy
}

var (
	ErrUnauthorized = NewError(http.StatusUnauthorized, "unauthorized", "请先登录。")
	ErrForbidden    = NewError(http.StatusForbidden, "forbidden", "没有访问该资源的权限。")
	ErrNotFound     = NewError(http.StatusNotFound, "not_found", "资源不存在。")
	ErrConflict     = NewError(http.StatusConflict, "conflict", "数据已被其他修改更新，请刷新后重试。")
	ErrBadRequest   = NewError(http.StatusBadRequest, "bad_request", "请求内容不正确。")
	ErrUnavailable  = NewError(http.StatusServiceUnavailable, "unavailable", "服务暂时不可用。")
)

// JSON writes a JSON response. It is a no-op when the header was already sent.
func JSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if r != nil && r.Method == http.MethodHead {
		return
	}
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}

// Error renders an error envelope, translating unknown errors to 500.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	apiErr := AsAPIError(err)
	body := map[string]any{"code": apiErr.Code, "message": apiErr.Message}
	if apiErr.Details != nil {
		body["details"] = apiErr.Details
	}
	JSON(w, r, apiErr.Status, map[string]any{"error": body})
}

// AsAPIError maps domain and transport errors onto an APIError.
func AsAPIError(err error) *APIError {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return NewError(http.StatusGatewayTimeout, "timeout", "操作超时，请稍后重试。")
	case errors.Is(err, context.Canceled):
		return NewError(499, "cancelled", "请求已取消。")
	}
	return NewError(http.StatusInternalServerError, "internal", "服务器内部错误。")
}

// Fail is shorthand for Error with a constructed APIError.
func Fail(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	Error(w, r, NewError(status, code, message))
}

// Decode reads a bounded JSON body into dst.
func Decode(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	if r.Body == nil {
		return NewError(http.StatusBadRequest, "bad_request", "请求体为空。")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return NewError(http.StatusRequestEntityTooLarge, "too_large", "请求内容过大。")
		}
		if errors.Is(err, io.EOF) {
			return NewError(http.StatusBadRequest, "bad_request", "请求体为空。")
		}
		return NewError(http.StatusBadRequest, "bad_request", "请求格式不正确："+err.Error())
	}
	if decoder.More() {
		return NewError(http.StatusBadRequest, "bad_request", "请求体只能包含一个 JSON 对象。")
	}
	return nil
}

// ---------------------------------------------------------------- middleware

type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxActor
)

func RequestIDFrom(ctx context.Context) string {
	value, _ := ctx.Value(ctxRequestID).(string)
	return value
}

// SetActor records the authenticated user id for access logging.
func SetActor(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, ctxActor, userID)
}

func ActorFrom(ctx context.Context) string {
	value, _ := ctx.Value(ctxActor).(string)
	return value
}

func newRequestID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// Middleware is the standard decorator type.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so the first argument is the outermost layer.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// RequestID attaches an id from the caller or generates one.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-Id"))
		if id == "" || len(id) > 64 {
			id = newRequestID()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

// Recover converts a panic into a 500 envelope and logs it.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic", "error", fmt.Sprint(rec), "path", r.URL.Path, "request_id", RequestIDFrom(r.Context()))
					if rec == http.ErrAbortHandler {
						panic(rec)
					}
					Error(w, r, NewError(http.StatusInternalServerError, "internal", "服务器内部错误。"))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(p)
	s.bytes += n
	return n, err
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// AccessLog emits one structured line per request.
func AccessLog(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			attrs := []any{
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", RequestIDFrom(r.Context()),
			}
			if actor := ActorFrom(r.Context()); actor != "" {
				attrs = append(attrs, "user_id", actor)
			}
			// Health probes are frequent and uninteresting at info level.
			if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
				log.Debug("request", attrs...)
				return
			}
			log.Info("request", attrs...)
		})
	}
}

// SecurityHeaders sets the headers required by the deployed app.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// SameOrigin rejects state-changing cross-site requests. Session cookies are
// SameSite=Lax, and this adds a second explicit check for JSON APIs: when a
// browser sends an Origin header, its host and port must match the request
// Host exactly (a suffix match would accept lookalike origins).
func SameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
			if !sameOriginHost(origin, r.Host) {
				Fail(w, r, http.StatusForbidden, "cross_origin", "不允许跨站写入。")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// sameOriginHost reports whether an Origin header addresses exactly the host
// (including port) the request was sent to. Origin values are scheme://host[:port]
// per the Fetch standard; anything else is rejected.
func sameOriginHost(origin, host string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return strings.EqualFold(parsed.Host, host)
}

// MethodNotAllowed is a small helper for route handlers that must restrict verbs.
func MethodNotAllowed(w http.ResponseWriter, r *http.Request, allowed ...string) {
	w.Header().Set("Allow", strings.Join(allowed, ", "))
	Fail(w, r, http.StatusMethodNotAllowed, "method_not_allowed", "该接口不支持此请求方法。")
}

// ClientIP returns the caller address. Forwarded headers are only trusted when
// the deployment explicitly enables RKA_TRUST_PROXY.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
			parts := strings.Split(forwarded, ",")
			if ip := strings.TrimSpace(parts[0]); ip != "" {
				return ip
			}
		}
		if real := strings.TrimSpace(r.Header.Get("X-Real-IP")); real != "" {
			return real
		}
	}
	host := r.RemoteAddr
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}
	return host
}

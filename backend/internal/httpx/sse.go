package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// SSEWriter streams server-sent events with resumable ids.
type SSEWriter struct {
	w  http.ResponseWriter
	f  http.Flusher
	mu sync.Mutex
}

// NewSSE prepares the response for event streaming. It fails when the
// underlying writer cannot flush, because a non-flushing stream is unusable.
func NewSSE(w http.ResponseWriter) (*SSEWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("streaming unsupported")
	}
	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &SSEWriter{w: w, f: flusher}, nil
}

// Event writes one event. id is the resumable sequence number.
func (s *SSEWriter) Event(id string, name string, payload any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var body []byte
	if payload == nil {
		body = []byte("{}")
	} else {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = encoded
	}
	var buf []byte
	if id != "" {
		buf = append(buf, "id: "...)
		buf = append(buf, id...)
		buf = append(buf, '\n')
	}
	if name != "" {
		buf = append(buf, "event: "...)
		buf = append(buf, name...)
		buf = append(buf, '\n')
	}
	for _, line := range splitLines(body) {
		buf = append(buf, "data: "...)
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	buf = append(buf, '\n')
	if _, err := s.w.Write(buf); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

func (s *SSEWriter) Comment(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	s.f.Flush()
	return nil
}

// KeepAlive writes comment frames until the context is done so proxies do not
// close an idle stream.
func (s *SSEWriter) KeepAlive(done <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := s.Comment("ping"); err != nil {
				return
			}
		}
	}
}

func splitLines(body []byte) []string {
	if len(body) == 0 {
		return []string{""}
	}
	var lines []string
	start := 0
	for i, b := range body {
		if b == '\n' {
			lines = append(lines, string(body[start:i]))
			start = i + 1
		}
	}
	if start <= len(body)-1 {
		lines = append(lines, string(body[start:]))
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}

// CopyJSONStream relays a JSON body into the response (used by the model
// adapter for non-streaming fallbacks). It keeps the caller's status code.
func CopyJSONStream(w http.ResponseWriter, body io.Reader, status int) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, err := io.Copy(w, body)
	return err
}

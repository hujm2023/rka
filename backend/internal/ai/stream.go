package ai

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"rka/internal/auth"
	"rka/internal/httpx"
)

const (
	// ssePollInterval bounds how quickly a worker-written event reaches an open
	// browser stream. It is a database poll, not an external call.
	ssePollInterval = 150 * time.Millisecond
	sseKeepAlive    = 15 * time.Second
	sseBatchSize    = 200
)

// handleEvents streams persisted task events and resumes from ?after=N or the
// Last-Event-ID header. Every event id is the durable per-task sequence, so a
// reconnecting browser can never miss or duplicate an event.
func (s *Service) handleEvents(w http.ResponseWriter, r *http.Request) {
	user := auth.MustUser(r.Context())
	task, err := s.TaskForUser(r.Context(), user.ID, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, mapTaskError(err))
		return
	}
	after := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("after")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			httpx.Error(w, r, httpx.NewError(http.StatusBadRequest, "bad_request", "after 必须是非负整数。"))
			return
		}
		after = parsed
	} else if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			after = parsed
		}
	}

	stream, err := httpx.NewSSE(w)
	if err != nil {
		httpx.Error(w, r, httpx.NewError(http.StatusInternalServerError, "stream_unsupported", "当前连接不支持事件流。"))
		return
	}
	ctx := r.Context()
	lastSent := after
	lastComment := time.Now()
	ticker := time.NewTicker(ssePollInterval)
	defer ticker.Stop()
	for {
		events, err := s.EventsAfter(ctx, task.ID, lastSent, sseBatchSize)
		if err != nil {
			return
		}
		for _, event := range events {
			if err := stream.Event(strconv.Itoa(event.Seq), "task", map[string]any{
				"seq":       event.Seq,
				"type":      event.Type,
				"payload":   event.Payload,
				"createdAt": event.CreatedAt,
			}); err != nil {
				return
			}
			lastSent = event.Seq
		}
		if len(events) == sseBatchSize {
			continue // drain a long backlog before waiting
		}
		current, err := s.LoadTask(ctx, task.ID)
		if err != nil {
			return // task deleted or database gone: closing the stream is honest
		}
		if isStreamTerminal(current.Status) && len(events) == 0 {
			return
		}
		if time.Since(lastComment) >= sseKeepAlive {
			if err := stream.Comment("ping"); err != nil {
				return
			}
			lastComment = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// isStreamTerminal reports states after which no further events can appear.
// waiting_user pauses the stream: the next user message starts a new turn.
func isStreamTerminal(status string) bool {
	switch status {
	case StatusCompleted, StatusFailed, StatusCancelled, StatusWaitingUser:
		return true
	}
	return false
}

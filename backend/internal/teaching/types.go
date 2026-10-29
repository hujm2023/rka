package teaching

import (
	"context"
	"errors"
	"time"
)

// Identity is one RKA user's private teaching identity. The credential is a
// generated secret used only between RKA and the teaching backend; it is never
// the RKA login password and it is never serialised to clients.
type Identity struct {
	UserID         string     `json:"userId"`
	TeachingUserID string     `json:"teachingUserId"`
	Credential     string     `json:"-"`
	CreatedAt      time.Time  `json:"createdAt"`
	RotatedAt      *time.Time `json:"rotatedAt,omitempty"`
}

// Source is one verifiable reference unit offered to the model. Source IDs are
// short labels (S0, S1...) so responses can cite them as [S0].
type Source struct {
	MaterialVersionID string `json:"materialVersionId,omitempty"`
	ID                string `json:"id"`
	Title             string `json:"title"`
	Text              string `json:"text"`
	MaterialID        string `json:"materialId,omitempty"`
	SectionID         string `json:"sectionId,omitempty"`
	SectionKey        string `json:"sectionKey,omitempty"`
}

// Message is one turn of an ongoing conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// TurnRequest is everything the adapters need to execute one teaching turn.
type TurnRequest struct {
	KnowledgeBases []string
	SubmissionID   string
	OnStarted      func(TurnRef) error
	CommitFrame    func(int64, []Event) error
	Identity       Identity
	TaskID         string
	Kind           string
	Prompt         string
	Hint           bool
	Context        map[string]any
	Sources        []Source
	History        []Message
	// ResumeReply is non-empty when the turn continues a DeepTutor turn that
	// previously asked the user a question. Stateless backends ignore it and
	// start a fresh turn that carries the full history instead.
	ResumeReply string
}

// Event is one normalized teaching event before it is persisted by the AI
// task layer. Types are accepted/progress/delta/citation/question/done/error/cancelled.
type Event struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// Result is the terminal outcome of a streamed turn.
type Result struct {
	UpstreamSeq int64          `json:"upstreamSeq,omitempty"`
	TurnID      string         `json:"turnId"`
	SessionID   string         `json:"sessionId"`
	FinalText   string         `json:"finalText"`
	Waiting     bool           `json:"waiting"`
	Usage       map[string]any `json:"usage,omitempty"`
}

// TurnRef identifies an upstream turn so it can be streamed, cancelled or
// queried after a reconnect.
type TurnRef struct {
	AfterSeq  int64
	Identity  Identity
	TaskID    string
	TurnID    string
	SessionID string
}

// RecoveryState describes what an upstream service reports about a turn that
// RKA lost contact with. It is deliberately explicit: "unknown" must never be
// treated as "failed" because that would invite a blind retry.
type RecoveryState string

const (
	RecoveryCompleted RecoveryState = "completed"
	RecoveryRunning   RecoveryState = "running"
	RecoveryWaiting   RecoveryState = "waiting_user"
	RecoveryFailed    RecoveryState = "failed"
	RecoveryNotFound  RecoveryState = "not_found"
	RecoveryUnknown   RecoveryState = "unknown"
)

// Recovery is the answer to "did the upstream accept and finish the turn?".
type Recovery struct {
	State  RecoveryState
	Result *Result
	Reason string
}

// Backend is the adapter contract for one teaching execution backend.
// Implementations: DeepTutor over HTTP and an OpenAI-compatible gateway.
type Backend interface {
	// Name is a stable identifier used in status output.
	Name() string
	// Configured reports whether the backend has enough configuration to run.
	Configured() bool
	// Start creates an upstream turn.
	Start(ctx context.Context, req TurnRequest) (TurnRef, error)
	// Stream relays the turn's events until it finishes or fails. The request is
	// passed again because stateless backends build their payload at stream time.
	Stream(ctx context.Context, req TurnRequest, ref TurnRef, emit func(Event) error) (Result, error)
	// Reply posts a user reply into an existing turn when the backend supports
	// it. Stateless backends return ErrReplyUnsupported.
	Reply(ctx context.Context, ref TurnRef, text string) error
	// Cancel asks the upstream to stop the turn. It is best effort.
	Cancel(ctx context.Context, ref TurnRef) error
	// Recover asks the upstream about a turn whose outcome is unknown.
	Recover(ctx context.Context, ref TurnRef) (Recovery, error)
}

// ErrUnavailable means the selected backend is not configured at all.
var ErrUnavailable = errors.New("teaching backend is not configured")

// ErrReplyUnsupported means the backend is stateless and cannot append a reply
// to an existing turn; callers should start a new turn with full history.
var ErrReplyUnsupported = errors.New("teaching backend does not support turn replies")

// ErrCancelled means the local caller aborted the turn.
var ErrCancelled = errors.New("teaching turn cancelled")

// ErrUpstreamUnknown marks a failure after the request may have reached the
// upstream. The turn must be marked recovering and verified before any retry.
var ErrUpstreamUnknown = errors.New("teaching upstream state is unknown")

// UpstreamError is returned for an upstream HTTP/stream failure. It separates
// "safe to retry" from "the upstream may have accepted the work".
type UpstreamError struct {
	Code       string
	Message    string
	Status     int
	Retryable  bool
	Unknown    bool
	Transient  bool
	Cancelled  bool
	Underlying error
}

func (e *UpstreamError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return "teaching upstream error"
}

func (e *UpstreamError) Unwrap() error {
	if e.Underlying != nil {
		return e.Underlying
	}
	return nil
}

// IsRetryable reports whether a turn can safely be attempted again.
func IsRetryable(err error) bool {
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		return upstream.Retryable && !upstream.Unknown
	}
	return false
}

// IsUnknownState reports whether the upstream may have received the request.
func IsUnknownState(err error) bool {
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		return upstream.Unknown
	}
	return errors.Is(err, ErrUpstreamUnknown)
}

// IsCancelled reports whether the caller aborted the turn.
func IsCancelled(err error) bool {
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		return upstream.Cancelled
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, ErrCancelled)
}

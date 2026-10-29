// Package teaching adapts RKA's teaching contract to an execution backend.
//
// RKA owns identity, task state and persistence. The adapter owns only the
// upstream conversation: mapping one RKA user to one private teaching identity,
// creating turns, relaying events, posting replies, cancelling and recovering
// turns after a disconnect. When DeepTutor is not configured the adapter can
// execute turns through an OpenAI-compatible gateway so a teaching round stays
// verifiable instead of silently disappearing.
package teaching

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"rka/internal/config"
	"rka/internal/store"
)

// Deps wires the teaching adapter. DeepTutor is an independent service: RKA
// keeps its own identity, credentials and task state.
type Deps struct {
	Config *config.Config
	DB     *store.DB
	Log    *slog.Logger
}

// deeptutorBackend is the DeepTutor adapter contract used by Status: it adds a
// health probe and a credential-free base URL to the generic Backend contract.
type deeptutorBackend interface {
	Backend
	Health(ctx context.Context) (deepTutorHealth, error)
	SafeBaseURL() string
}

type Service struct {
	nativeMu        sync.Mutex
	nativeSignature string
	cfg             *config.Config
	db              *store.DB
	log             *slog.Logger
	deepTutor       deeptutorBackend
	gateway         *gatewayBackend

	statusMu sync.Mutex
	statusAt time.Time
	cached   Status
}

func New(deps Deps) *Service {
	service := &Service{
		cfg: deps.Config,
		db:  deps.DB,
		log: deps.Log,
	}
	if url := strings.TrimSpace(deps.Config.Teaching.DeepTutorURL); url != "" {
		if deps.Config.Teaching.DeepTutorToken != "" {
			// Static-token mode is a test double / legacy deployment. The pinned
			// DeepTutor 1.6.15 has no static API token and uses JWT + WebSocket.
			service.deepTutor = newDeepTutorBackend(
				url,
				deps.Config.Teaching.DeepTutorToken,
				deps.Config.AI.RequestTimeout,
				deps.Log,
			)
		} else {
			service.deepTutor = newDeepTutorWSBackend(
				url,
				deps.Config.Teaching.DeepTutorAdminUsername,
				deps.Config.Teaching.DeepTutorAdminPassword,
				deps.Config.AI.RequestTimeout,
				deps.Log,
			)
		}
	}
	service.gateway = newGatewayBackend(deps.Config.AI, deps.Log)
	return service
}

// Status is the honest teaching-service state: configured is not the same as
// reachable, and reachable is not the same as capability-verified. This method
// reports the DeepTutor dependency; execution readiness also accounts for the
// OpenAI-compatible fallback.
type Status struct {
	Configured     bool   `json:"configured"`
	Reachable      bool   `json:"reachable"`
	Reason         string `json:"reason"`
	BaseURL        string `json:"baseUrl"`
	EmbeddingReady bool   `json:"embeddingReady"`
	Version        string `json:"version"`
	// Backend is the adapter that would execute the next teaching turn:
	// "deeptutor", "gateway" or "none".
	Backend string `json:"backend"`
	// ExecutionReady is true when at least one backend can execute a turn.
	ExecutionReady bool `json:"executionReady"`
}

const statusCacheTTL = 3 * time.Second

// Status probes the teaching service at most once every few seconds. A health
// probe is intentionally cheap and short so a down dependency cannot stall the
// page that reports it.
func (s *Service) Status(ctx context.Context) Status {
	s.statusMu.Lock()
	if !s.statusAt.IsZero() && time.Since(s.statusAt) < statusCacheTTL {
		cached := s.cached
		s.statusMu.Unlock()
		return cached
	}
	s.statusMu.Unlock()

	status := s.probeStatus(ctx)

	s.statusMu.Lock()
	s.cached = status
	s.statusAt = time.Now()
	s.statusMu.Unlock()
	return status
}

func (s *Service) probeStatus(ctx context.Context) Status {
	status := Status{
		Configured:     s.deepTutor != nil && s.deepTutor.Configured(),
		BaseURL:        s.safeBaseURL(),
		EmbeddingReady: false,
	}
	_ = s.db.Pool.QueryRow(ctx, `select exists(select 1 from knowledge_indexes where status='ready')`).Scan(&status.EmbeddingReady)
	gatewayReady := s.gateway != nil && s.gateway.Configured()
	switch {
	case status.Configured:
		status.Backend = "deeptutor"
		info, err := s.deepTutor.Health(ctx)
		if err != nil {
			status.Reachable = false
			status.Reason = "DeepTutor 教学服务连接失败：" + err.Error()
			if gatewayReady {
				status.Reason += "；如需临时改用 OpenAI 兼容网关，请清空 RKA_DEEPTUTOR_URL。"
			}
		} else {
			status.Reachable = true
			status.Version = info.Version
		}
		status.ExecutionReady = status.Reachable
		if ws, ok := s.deepTutor.(*deepTutorWSBackend); ok {
			if !ws.adminConfigured() {
				status.ExecutionReady = false
				status.Reason = "未配置教学服务管理员凭据。"
			} else if status.Reachable {
				token, err := ws.adminToken(ctx)
				if err != nil {
					status.ExecutionReady = false
					status.Reason = err.Error()
				} else {
					profile, model, err := ws.activeModel(ctx, token)
					if err != nil {
						status.ExecutionReady = false
						status.Reason = "无法读取教学模型配置。"
					} else if profile == "" || model == "" {
						status.ExecutionReady = false
						status.Reason = "未配置教学模型；原文、刷题和写作仍可使用。"
					}
				}
			}
		}
	case gatewayReady:
		status.Backend = "gateway"
		status.ExecutionReady = true
		status.Reason = "未配置 DeepTutor 教学服务（RKA_DEEPTUTOR_URL 为空），当前由 OpenAI 兼容网关执行教学任务。"
	default:
		status.Backend = "none"
		status.Reason = "未配置 AI 模型（RKA_AI_ENDPOINT / RKA_AI_MODEL / RKA_AI_API_KEY）或 DeepTutor 教学服务（RKA_DEEPTUTOR_URL），AI 辅导不可用。基础学习功能不受影响。"
	}
	return status
}

func (s *Service) safeBaseURL() string {
	if s.deepTutor == nil {
		return ""
	}
	return s.deepTutor.SafeBaseURL()
}

// UnavailableReason returns the user-facing reason when no teaching backend can
// execute a turn, or an empty string when one is ready.
func (s *Service) UnavailableReason(ctx context.Context) string {
	status := s.Status(ctx)
	if status.ExecutionReady {
		return ""
	}
	return status.Reason
}

// BackendName reports the selected execution backend for status output.
func (s *Service) BackendName(ctx context.Context) string {
	return s.Status(ctx).Backend
}

// activeBackend selects the execution adapter. DeepTutor is authoritative when
// configured; the gateway is the fallback so a deployment without DeepTutor can
// still complete a real teaching round. Selection never silently swaps a
// configured-but-broken DeepTutor for another backend.
func (s *Service) activeBackend() (Backend, error) {
	if s.deepTutor != nil && s.deepTutor.Configured() {
		return s.deepTutor, nil
	}
	if s.gateway != nil && s.gateway.Configured() {
		return s.gateway, nil
	}
	return nil, ErrUnavailable
}

// ---------------------------------------------------------------- identities

// Identity returns (creating on first use) the private teaching identity for an
// RKA user. The generated credential is encrypted with AES-GCM before it is
// written to teaching_identities and is never returned to a browser.
func (s *Service) Identity(ctx context.Context, userID string) (Identity, error) {
	if strings.TrimSpace(userID) == "" {
		return Identity{}, errors.New("teaching identity requires a user id")
	}
	identity, err := s.loadIdentity(ctx, userID)
	if err == nil {
		return identity, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return Identity{}, err
	}

	teachingUserID := "rka-" + randomHex(12)
	credential := randomToken()
	ciphertext, err := encryptCredential(s.cfg.SecretKey, credential)
	if err != nil {
		return Identity{}, err
	}
	_, err = s.db.Pool.Exec(ctx, `
		insert into teaching_identities (user_id, teaching_user_id, credential_ciphertext)
		values ($1::uuid, $2, $3)
		on conflict (user_id) do nothing`, userID, teachingUserID, ciphertext)
	if err != nil {
		if store.IsForeignKeyViolation(err) {
			return Identity{}, fmt.Errorf("teaching identity user %s does not exist", userID)
		}
		return Identity{}, fmt.Errorf("create teaching identity: %w", err)
	}
	identity, err = s.loadIdentity(ctx, userID)
	if err != nil {
		return Identity{}, err
	}
	s.log.Info("teaching identity created", "user_id", userID, "teaching_user_id", identity.TeachingUserID)
	return identity, nil
}

func (s *Service) loadIdentity(ctx context.Context, userID string) (Identity, error) {
	var (
		identity   Identity
		ciphertext string
		createdAt  time.Time
		rotatedAt  *time.Time
	)
	err := s.db.Pool.QueryRow(ctx, `
		select user_id::text, teaching_user_id, credential_ciphertext, created_at, rotated_at
		from teaching_identities where user_id = $1::uuid`, userID).
		Scan(&identity.UserID, &identity.TeachingUserID, &ciphertext, &createdAt, &rotatedAt)
	if err != nil {
		return Identity{}, store.MapError(err)
	}
	credential, err := decryptCredential(s.cfg.SecretKey, ciphertext)
	if err != nil {
		return Identity{}, fmt.Errorf("teaching identity for %s: %w", userID, err)
	}
	identity.Credential = credential
	identity.CreatedAt = createdAt
	identity.RotatedAt = rotatedAt
	return identity, nil
}

// ---------------------------------------------------------------- execution

// PrepareTurn builds the prompt, history and verifiable sources for a task.
// Sources are loaded from RKA's own content tables; callers cannot inject
// arbitrary "reference text" through the request body.
func (s *Service) PrepareTurn(ctx context.Context, in PrepareInput) (TurnRequest, error) {
	return s.prepareTurn(ctx, in)
}

// RunTurn starts (or resumes) a teaching turn and streams normalized events.
func (s *Service) RunTurn(ctx context.Context, req TurnRequest, resume *TurnRef, emit func(Event) error) (TurnRef, Result, error) {
	backend, err := s.activeBackend()
	if err != nil {
		return TurnRef{}, Result{}, err
	}
	if err := s.authorizeKnowledge(ctx, req); err != nil {
		return TurnRef{}, Result{}, err
	}
	ref := TurnRef{Identity: req.Identity, TaskID: req.TaskID}
	if resume != nil {
		ref = *resume
	}
	resumed := false
	if resume != nil && resume.TurnID != "" && req.ResumeReply != "" {
		if err := backend.Reply(ctx, ref, req.ResumeReply); err != nil {
			if !errors.Is(err, ErrReplyUnsupported) {
				return ref, Result{}, err
			}
		} else {
			resumed = true
		}
	}
	if !resumed {
		ref, err = backend.Start(ctx, req)
		if err != nil {
			return ref, Result{}, err
		}
	}
	if req.OnStarted != nil {
		if err := req.OnStarted(ref); err != nil {
			return ref, Result{}, err
		}
	}
	originalCommit := req.CommitFrame
	if originalCommit != nil {
		req.CommitFrame = func(seq int64, events []Event) error { return originalCommit(seq, s.bindFrames(ctx, req, events)) }
	}
	originalEmit := emit
	emit = func(event Event) error {
		for _, bound := range s.bindFrames(ctx, req, []Event{event}) {
			if err := originalEmit(bound); err != nil {
				return err
			}
		}
		return nil
	}
	result, err := backend.Stream(ctx, req, ref, emit)
	ref.AfterSeq = result.UpstreamSeq
	return ref, result, err
}

// Recover asks the upstream about a turn whose outcome RKA could not observe.
func (s *Service) Recover(ctx context.Context, ref TurnRef) (Recovery, error) {
	backend, err := s.activeBackend()
	if err != nil {
		return Recovery{State: RecoveryUnknown, Reason: err.Error()}, nil
	}
	return backend.Recover(ctx, ref)
}

// Cancel stops an upstream turn. Failure is reported so the caller can keep the
// task in cancelling instead of pretending the upstream stopped.
func (s *Service) Cancel(ctx context.Context, ref TurnRef) error {
	backend, err := s.activeBackend()
	if err != nil {
		return err
	}
	return backend.Cancel(ctx, ref)
}

// Reply posts a user reply to an existing upstream turn when supported.
func (s *Service) Reply(ctx context.Context, ref TurnRef, text string) error {
	backend, err := s.activeBackend()
	if err != nil {
		return err
	}
	return backend.Reply(ctx, ref, text)
}

// ---------------------------------------------------------------- helpers

func randomHex(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		panic("teaching: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(buf)
}

func randomToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic("teaching: crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

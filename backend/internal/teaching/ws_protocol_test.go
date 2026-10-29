package teaching_test

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"rka/internal/config"
	"rka/internal/teaching"
)

const wsTestGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// fakeDeepTutorWS is a protocol-level test double for DeepTutor 1.6.15: JWT
// cookie login, admin user creation and WebSocket turn protocol v2.0. It does
// not call any real model.
type fakeDeepTutorWS struct {
	server *httptest.Server
	mu     sync.Mutex

	users         map[string]string
	tokens        map[string]string
	replayHistory bool
	waitFirst     bool
	answered      bool
	activeTurn    string
	started       int
	replies       []string
	commands      []string
}

func newFakeDeepTutorWS(t *testing.T) *fakeDeepTutorWS {
	t.Helper()
	fake := &fakeDeepTutorWS{
		users:  map[string]string{"rka-admin": "admin-pass"},
		tokens: map[string]string{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ready","version":"1.6.15-fake"}`)
	})
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		fake.mu.Lock()
		expected, ok := fake.users[payload.Username]
		if ok && expected == payload.Password {
			token := "token-" + payload.Username
			fake.tokens[token] = payload.Username
			fake.mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "dt_token", Value: token, Path: "/"})
			_, _ = io.WriteString(w, `{"ok":true}`)
			return
		}
		fake.mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"detail":"bad credentials"}`)
	})
	mux.HandleFunc("POST /api/auth/users", func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		fake.mu.Lock()
		_, admin := fake.tokens[token]
		fake.mu.Unlock()
		if !admin {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var payload struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Preset   string `json:"preset"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		fake.mu.Lock()
		if _, exists := fake.users[payload.Username]; exists {
			fake.mu.Unlock()
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"detail":"exists"}`)
			return
		}
		fake.users[payload.Username] = payload.Password
		fake.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"ok":true}`)
	})
	mux.HandleFunc("GET /ws", fake.handleWS)
	fake.server = httptest.NewServer(mux)
	t.Cleanup(fake.server.Close)
	return fake
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if strings.HasPrefix(header, "Bearer ") {
		return strings.TrimPrefix(header, "Bearer ")
	}
	if cookie, err := r.Cookie("dt_token"); err == nil {
		return cookie.Value
	}
	return ""
}

func (f *fakeDeepTutorWS) handleWS(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("token") != "" {
		http.Error(w, "JWT must not be sent in a URL", http.StatusBadRequest)
		return
	}
	token := ""
	if cookie, err := r.Cookie("dt_token"); err == nil {
		token = cookie.Value
	}
	f.mu.Lock()
	_, valid := f.tokens[token]
	f.mu.Unlock()
	if !valid {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	key := r.Header.Get("Sec-WebSocket-Key")
	fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", wsTestAccept(key))
	for {
		opcode, payload, err := readClientWSFrame(rw.Reader)
		if err != nil {
			return
		}
		if opcode == 0x8 {
			return
		}
		if opcode != 0x1 {
			continue
		}
		var command map[string]any
		if err := json.Unmarshal(payload, &command); err != nil {
			return
		}
		f.handleCommand(conn, command)
	}
}

func (f *fakeDeepTutorWS) handleCommand(conn net.Conn, command map[string]any) {
	commandType, _ := command["type"].(string)
	f.mu.Lock()
	f.commands = append(f.commands, commandType)
	switch commandType {
	case "start_turn":
		f.started++
		f.activeTurn = fmt.Sprintf("turn-%d", f.started)
		turn := f.activeTurn
		content, _ := command["content"].(string)
		hasPrompt := strings.Contains(content, "系统指令")
		f.mu.Unlock()
		if !hasPrompt {
			f.mu.Lock()
			f.commands = append(f.commands, "missing-system-prompt")
			f.mu.Unlock()
		}
		writeServerWSFrame(conn, map[string]any{
			"type": "session", "session_id": "sess-1", "turn_id": turn, "seq": 1,
		})
	case "subscribe_turn":
		turn, _ := command["turn_id"].(string)
		waiting := f.waitFirst && !f.answered
		answered := f.answered
		replayHistory := f.replayHistory
		afterSeq, _ := command["after_seq"].(float64)
		f.mu.Unlock()
		writeServerWSFrame(conn, map[string]any{"type": "session", "session_id": "sess-1", "turn_id": turn, "seq": 1})
		writeServerWSFrame(conn, map[string]any{"type": "content", "content": "你好，", "seq": 2})
		writeServerWSFrame(conn, map[string]any{
			"type": "sources", "seq": 3,
			"metadata": map[string]any{"sources": []any{
				map[string]any{"id": "S0", "title": "资料一", "material_id": "m1", "section_id": "sec-1"},
			}},
		})
		if waiting {
			writeServerWSFrame(conn, map[string]any{"type": "wait_for_input", "content": "你认为关键在哪里？", "seq": 4})
			return
		}
		if f.waitFirst && !answered {
			writeServerWSFrame(conn, map[string]any{"type": "wait_for_input", "content": "请先回答。", "seq": 4})
			return
		}
		if replayHistory && answered && afterSeq < 4 {
			writeServerWSFrame(conn, map[string]any{"type": "wait_for_input", "content": "你认为关键在哪里？", "seq": 4})
		}
		writeServerWSFrame(conn, map[string]any{"type": "content", "content": "这是讲解 [S0]。", "seq": 5})
		writeServerWSFrame(conn, map[string]any{"type": "done", "seq": 6, "metadata": map[string]any{"status": "completed"}})
	case "submit_user_reply":
		text, _ := command["text"].(string)
		f.replies = append(f.replies, text)
		f.answered = true
		f.mu.Unlock()
		writeServerWSFrame(conn, map[string]any{"type": "command_ack", "accepted": true, "command_id": command["command_id"]})
	case "cancel_turn":
		f.activeTurn = ""
		f.mu.Unlock()
		writeServerWSFrame(conn, map[string]any{"type": "command_ack", "accepted": true, "command_id": command["command_id"]})
	case "check_active_turn":
		active := f.activeTurn
		f.mu.Unlock()
		writeServerWSFrame(conn, map[string]any{"type": "active_turn_info", "turn_id": active, "session_id": "sess-1"})
	default:
		f.mu.Unlock()
		writeServerWSFrame(conn, map[string]any{"type": "command_ack", "accepted": true, "command_id": command["command_id"]})
	}
}

func wsTestAccept(key string) string {
	sum := sha1.Sum([]byte(key + wsTestGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func readClientWSFrame(reader *bufio.Reader) (byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}
	opcode := header[0] & 0x0F
	masked := header[1]&0x80 != 0
	length := int(header[1] & 0x7F)
	switch length {
	case 126:
		size := make([]byte, 2)
		if _, err := io.ReadFull(reader, size); err != nil {
			return 0, nil, err
		}
		length = int(binary.BigEndian.Uint16(size))
	case 127:
		size := make([]byte, 8)
		if _, err := io.ReadFull(reader, size); err != nil {
			return 0, nil, err
		}
		length = int(binary.BigEndian.Uint64(size))
	}
	var mask []byte
	if masked {
		mask = make([]byte, 4)
		if _, err := io.ReadFull(reader, mask); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func writeServerWSFrame(conn net.Conn, payload any) {
	encoded, _ := json.Marshal(payload)
	frame := []byte{0x81}
	length := len(encoded)
	switch {
	case length < 126:
		frame = append(frame, byte(length))
	case length <= 65535:
		frame = append(frame, 126, byte(length>>8), byte(length))
	default:
		frame = append(frame, 127)
		size := make([]byte, 8)
		binary.BigEndian.PutUint64(size, uint64(length))
		frame = append(frame, size...)
	}
	frame = append(frame, encoded...)
	_, _ = conn.Write(frame)
}

// ---------------------------------------------------------------- tests

func TestDeepTutorWSProtocolTurnLifecycle(t *testing.T) {
	fake := newFakeDeepTutorWS(t)
	service, db := newTestService(t, func(cfg *config.Config) {
		cfg.Teaching.DeepTutorURL = fake.server.URL
		cfg.Teaching.DeepTutorAdminUsername = "rka-admin"
		cfg.Teaching.DeepTutorAdminPassword = "admin-pass"
	})
	userID := seedUser(t, db, "ws@example.com")
	identity, err := service.Identity(t.Context(), userID)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}

	status := service.Status(t.Context())
	if !status.Configured || !status.Reachable || status.Version != "1.6.15-fake" {
		t.Fatalf("status = %+v", status)
	}

	request := teaching.TurnRequest{
		Identity: identity, TaskID: "task-ws-1", Kind: "explain", Prompt: "解释这个知识点",
		Sources: []teaching.Source{{ID: "S0", Title: "资料一", Text: "内容", MaterialID: "m1", SectionID: "sec-1"}},
	}
	var events []teaching.Event
	ref, result, err := service.RunTurn(t.Context(), request, nil, func(event teaching.Event) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("run turn: %v", err)
	}
	if ref.TurnID != "turn-1" || ref.SessionID != "sess-1" {
		t.Fatalf("turn ref = %+v", ref)
	}
	if result.FinalText != "你好，这是讲解 [S0]。" {
		t.Fatalf("final text = %q", result.FinalText)
	}
	var types []string
	for _, event := range events {
		types = append(types, event.Type)
	}
	if strings.Join(types, ",") != "delta,citation,delta" {
		t.Fatalf("events = %v", types)
	}
	fake.mu.Lock()
	commands := append([]string(nil), fake.commands...)
	createdUser, userExists := fake.users[identity.TeachingUserID]
	fake.mu.Unlock()
	if !userExists || createdUser != identity.Credential {
		t.Fatal("per-user DeepTutor account was not created with the generated credential")
	}
	joined := strings.Join(commands, ",")
	if !strings.Contains(joined, "start_turn") || !strings.Contains(joined, "subscribe_turn") {
		t.Fatalf("ws commands = %v", commands)
	}
	if strings.Contains(joined, "missing-system-prompt") {
		t.Fatal("start_turn did not carry the protected RKA prompt")
	}

	recovery, err := service.Recover(t.Context(), ref)
	if err != nil || recovery.State != teaching.RecoveryCompleted || recovery.Result.FinalText != "你好，这是讲解 [S0]。" {
		t.Fatalf("recovery = %+v, %v", recovery, err)
	}
	if err := service.Cancel(t.Context(), ref); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	fake.mu.Lock()
	cancelled := false
	for _, command := range fake.commands {
		if command == "cancel_turn" {
			cancelled = true
		}
	}
	fake.mu.Unlock()
	if !cancelled {
		t.Fatal("cancel_turn command was not sent")
	}
}

func TestDeepTutorWSWaitingAndReply(t *testing.T) {
	fake := newFakeDeepTutorWS(t)
	fake.mu.Lock()
	fake.waitFirst = true
	fake.mu.Unlock()
	service, db := newTestService(t, func(cfg *config.Config) {
		cfg.Teaching.DeepTutorURL = fake.server.URL
		cfg.Teaching.DeepTutorAdminUsername = "rka-admin"
		cfg.Teaching.DeepTutorAdminPassword = "admin-pass"
	})
	userID := seedUser(t, db, "ws-wait@example.com")
	identity, err := service.Identity(t.Context(), userID)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	request := teaching.TurnRequest{Identity: identity, TaskID: "task-ws-2", Kind: "hint", Prompt: "给个提示"}
	var sawQuestion bool
	ref, result, err := service.RunTurn(t.Context(), request, nil, func(event teaching.Event) error {
		if event.Type == "question" {
			sawQuestion = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	if !sawQuestion || !result.Waiting {
		t.Fatalf("waiting result = %+v, saw=%v", result, sawQuestion)
	}

	request.ResumeReply = "我觉得是分层"
	if _, _, err := service.RunTurn(t.Context(), request, &ref, func(teaching.Event) error { return nil }); err != nil {
		t.Fatalf("resume turn: %v", err)
	}
	fake.mu.Lock()
	replies := append([]string(nil), fake.replies...)
	fake.mu.Unlock()
	if len(replies) != 1 || replies[0] != "我觉得是分层" {
		t.Fatalf("ws replies = %v", replies)
	}
}

func TestDeepTutorWSAuthFailureIsVisible(t *testing.T) {
	fake := newFakeDeepTutorWS(t)
	service, db := newTestService(t, func(cfg *config.Config) {
		cfg.Teaching.DeepTutorURL = fake.server.URL
		cfg.Teaching.DeepTutorAdminUsername = "rka-admin"
		cfg.Teaching.DeepTutorAdminPassword = "wrong-password"
	})
	userID := seedUser(t, db, "ws-auth@example.com")
	identity, err := service.Identity(t.Context(), userID)
	if err != nil {
		t.Fatalf("identity: %v", err)
	}
	_, _, err = service.RunTurn(t.Context(), teaching.TurnRequest{
		Identity: identity, TaskID: "task-ws-auth", Kind: "explain", Prompt: "解释",
	}, nil, func(teaching.Event) error { return nil })
	if err == nil {
		t.Fatal("expected an authentication error")
	}
	if !strings.Contains(err.Error(), "登录失败") {
		t.Fatalf("error = %v", err)
	}
}

func TestDeepTutorWSHealthUnreachable(t *testing.T) {
	service, _ := newTestService(t, func(cfg *config.Config) {
		cfg.Teaching.DeepTutorURL = "http://127.0.0.1:1"
		cfg.Teaching.DeepTutorAdminUsername = "rka-admin"
		cfg.Teaching.DeepTutorAdminPassword = "admin-pass"
	})
	status := service.Status(t.Context())
	if !status.Configured || status.Reachable || status.ExecutionReady || status.Reason == "" {
		t.Fatalf("status = %+v", status)
	}
}

var _ = time.Second

func TestResumeSkipsHistoricalWaitFrame(t *testing.T) {
	fake := newFakeDeepTutorWS(t)
	fake.mu.Lock()
	fake.waitFirst = true
	fake.replayHistory = true
	fake.mu.Unlock()
	service, db := newTestService(t, func(cfg *config.Config) {
		cfg.Teaching.DeepTutorURL = fake.server.URL
		cfg.Teaching.DeepTutorAdminUsername = "rka-admin"
		cfg.Teaching.DeepTutorAdminPassword = "admin-pass"
	})
	userID := seedUser(t, db, "review-replay@example.com")
	identity, err := service.Identity(t.Context(), userID)
	if err != nil {
		t.Fatal(err)
	}
	request := teaching.TurnRequest{Identity: identity, TaskID: "review-replay", Kind: "hint", Prompt: "请提示"}
	ref, initial, err := service.RunTurn(t.Context(), request, nil, func(teaching.Event) error { return nil })
	if err != nil || !initial.Waiting {
		t.Fatalf("initial = %+v, err=%v", initial, err)
	}
	request.ResumeReply = "我已经回答"
	_, result, err := service.RunTurn(t.Context(), request, &ref, func(teaching.Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	replies := append([]string(nil), fake.replies...)
	fake.mu.Unlock()
	t.Logf("upstream accepted replies=%v; resumed result=%+v", replies, result)
	if result.Waiting {
		t.Fatalf("reply was accepted, but replayed historical wait_for_input returned Waiting=true instead of consuming new answer and done")
	}
}

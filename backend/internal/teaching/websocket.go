package teaching

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"sync"
	"time"
)

// A minimal RFC 6455 WebSocket client. DeepTutor turn creation exists only on
// its WebSocket protocol (v2.0), and the module deliberately avoids adding a
// websocket dependency, so the small subset needed by the teaching adapter is
// implemented here: handshake, masked client frames, fragmented messages and
// ping/pong.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

var errWSClosed = errors.New("websocket closed by peer")

type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	mu   sync.Mutex

	closeOnce sync.Once
}

// dialWebSocket opens a ws:// or wss:// connection with optional extra headers.
func dialWebSocket(ctx context.Context, rawURL string, headers http.Header) (*wsConn, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("websocket url: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "ws" && scheme != "wss" {
		return nil, fmt.Errorf("websocket url must use ws or wss: %s", rawURL)
	}
	port := parsed.Port()
	if port == "" {
		if scheme == "wss" {
			port = "443"
		} else {
			port = "80"
		}
	}
	address := net.JoinHostPort(parsed.Hostname(), port)
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, classifyTransportError(err)
	}
	if scheme == "wss" {
		tlsConn := tls.Client(raw, &tls.Config{ServerName: parsed.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, classifyTransportError(err)
		}
		raw = tlsConn
	}

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		raw.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	requestURI := parsed.RequestURI()
	if requestURI == "" {
		requestURI = "/"
	}
	var request strings.Builder
	fmt.Fprintf(&request, "GET %s HTTP/1.1\r\n", requestURI)
	fmt.Fprintf(&request, "Host: %s\r\n", parsed.Host)
	request.WriteString("Upgrade: websocket\r\n")
	request.WriteString("Connection: Upgrade\r\n")
	fmt.Fprintf(&request, "Sec-WebSocket-Key: %s\r\n", key)
	request.WriteString("Sec-WebSocket-Version: 13\r\n")
	for name, values := range headers {
		switch strings.ToLower(name) {
		case "host", "upgrade", "connection", "sec-websocket-key", "sec-websocket-version":
			continue
		}
		for _, value := range values {
			fmt.Fprintf(&request, "%s: %s\r\n", name, value)
		}
	}
	request.WriteString("\r\n")
	if deadline, ok := ctx.Deadline(); ok {
		_ = raw.SetDeadline(deadline)
	}
	if _, err := io.WriteString(raw, request.String()); err != nil {
		raw.Close()
		return nil, classifyTransportError(err)
	}

	reader := bufio.NewReader(raw)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		raw.Close()
		return nil, classifyTransportError(err)
	}
	parts := strings.SplitN(strings.TrimSpace(statusLine), " ", 3)
	if len(parts) < 2 || parts[1] != "101" {
		raw.Close()
		return nil, &UpstreamError{Code: "teaching_ws_rejected", Message: "DeepTutor WebSocket 升级失败：" + strings.TrimSpace(statusLine)}
	}
	mimeReader := textproto.NewReader(reader)
	mimeHeader, err := mimeReader.ReadMIMEHeader()
	if err != nil {
		raw.Close()
		return nil, classifyTransportError(err)
	}
	expected := base64.StdEncoding.EncodeToString(sha1Sum(key + wsGUID))
	if accept := mimeHeader.Get("Sec-WebSocket-Accept"); accept != expected {
		raw.Close()
		return nil, &UpstreamError{Code: "teaching_ws_rejected", Message: "DeepTutor WebSocket 握手校验失败。"}
	}
	_ = raw.SetDeadline(time.Time{})
	return &wsConn{conn: raw, br: reader}, nil
}

func sha1Sum(value string) []byte {
	sum := sha1.Sum([]byte(value))
	return sum[:]
}

// CloseOnContext closes the connection when the context is cancelled so a
// blocked frame read cannot outlive a cancelled teaching turn.
func (c *wsConn) closeOnContext(ctx context.Context) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

func (c *wsConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		err = c.conn.Close()
	})
	return err
}

func (c *wsConn) SetReadDeadline(deadline time.Time) error {
	return c.conn.SetReadDeadline(deadline)
}

// WriteText sends one masked text frame.
func (c *wsConn) WriteText(payload []byte) error {
	return c.writeFrame(0x1, payload)
}

func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	frame := make([]byte, 0, len(payload)+14)
	frame = append(frame, 0x80|opcode)
	length := len(payload)
	switch {
	case length < 126:
		frame = append(frame, 0x80|byte(length))
	case length <= 65535:
		frame = append(frame, 0x80|126, byte(length>>8), byte(length))
	default:
		frame = append(frame, 0x80|127)
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(length))
		frame = append(frame, size[:]...)
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	frame = append(frame, mask...)
	for i, value := range payload {
		frame = append(frame, value^mask[i%4])
	}
	_, err := c.conn.Write(frame)
	return err
}

// ReadMessage returns the next complete text/binary message.
func (c *wsConn) ReadMessage() ([]byte, error) {
	var message []byte
	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x0: // continuation
			message = append(message, payload...)
		case 0x1, 0x2:
			message = append(message, payload...)
		case 0x8: // close
			_ = c.writeFrame(0x8, nil)
			return nil, errWSClosed
		case 0x9: // ping
			_ = c.writeFrame(0xA, payload)
			continue
		case 0xA: // pong
			continue
		default:
			continue
		}
		if fin {
			return message, nil
		}
	}
}

func (c *wsConn) readFrame() (bool, byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(c.br, header); err != nil {
		return false, 0, nil, err
	}
	fin := header[0]&0x80 != 0
	opcode := header[0] & 0x0F
	masked := header[1]&0x80 != 0
	length := int(header[1] & 0x7F)
	switch length {
	case 126:
		size := make([]byte, 2)
		if _, err := io.ReadFull(c.br, size); err != nil {
			return false, 0, nil, err
		}
		length = int(binary.BigEndian.Uint16(size))
	case 127:
		size := make([]byte, 8)
		if _, err := io.ReadFull(c.br, size); err != nil {
			return false, 0, nil, err
		}
		value := binary.BigEndian.Uint64(size)
		if value > 4<<20 {
			return false, 0, nil, errors.New("websocket frame too large")
		}
		length = int(value)
	}
	var mask []byte
	if masked {
		mask = make([]byte, 4)
		if _, err := io.ReadFull(c.br, mask); err != nil {
			return false, 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, opcode, payload, nil
}

// wsURL converts the HTTP base URL of DeepTutor to its WebSocket endpoint.
func wsURL(baseURL, token string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported DeepTutor URL scheme %q", parsed.Scheme)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + dtWSPath
	if token != "" {
		query := parsed.Query()
		query.Set("token", token)
		parsed.RawQuery = query.Encode()
	}
	if token == "" {
		parsed.RawQuery = ""
	}
	parsed.User = nil
	parsed.Fragment = ""
	return parsed.String(), nil
}

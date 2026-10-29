package teaching

import (
	"bufio"
	"io"
	"strings"
)

// sseFrame is one decoded Server-Sent Event frame.
type sseFrame struct {
	Event string
	Data  string
	ID    string
}

// readSSE decodes an event stream. It stops when the stream ends, the handler
// returns an error, or the reader fails. A frame is dispatched on a blank line,
// matching the SSE specification used by both supported backends.
func readSSE(reader io.Reader, handle func(sseFrame) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	frame := sseFrame{}
	var dataLines []string
	dispatch := func() error {
		if len(dataLines) == 0 && frame.Event == "" && frame.ID == "" {
			return nil
		}
		frame.Data = strings.Join(dataLines, "\n")
		err := handle(frame)
		frame = sseFrame{}
		dataLines = nil
		return err
	}
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if err := dispatch(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / keepalive
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			frame.Event = value
		case "data":
			dataLines = append(dataLines, value)
		case "id":
			frame.ID = value
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return dispatch()
}

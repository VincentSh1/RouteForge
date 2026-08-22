package provider

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

const MaxSSEEventSize = 64 << 10

var (
	ErrSSEEventTooLarge = errors.New("upstream SSE event too large")
	ErrSSERead          = errors.New("read upstream SSE event")
)

type SSEEvent struct {
	Type string
	Data []byte
}

type SSEReader struct {
	scanner *bufio.Scanner
	event   SSEEvent
}

func NewSSEReader(reader io.Reader) *SSEReader {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), MaxSSEEventSize)
	return &SSEReader{scanner: scanner}
}

func (r *SSEReader) Next() (SSEEvent, error) {
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if line == "" {
			if len(r.event.Data) == 0 && r.event.Type == "" {
				continue
			}
			return r.takeEvent(), nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			r.event.Type = value
		case "data":
			if len(r.event.Data) > 0 {
				r.event.Data = append(r.event.Data, '\n')
			}
			if len(r.event.Data)+len(value) > MaxSSEEventSize {
				return SSEEvent{}, ErrSSEEventTooLarge
			}
			r.event.Data = append(r.event.Data, value...)
		}
	}
	if err := r.scanner.Err(); err != nil {
		return SSEEvent{}, ErrSSERead
	}
	if len(r.event.Data) > 0 || r.event.Type != "" {
		return r.takeEvent(), nil
	}
	return SSEEvent{}, io.EOF
}

func (r *SSEReader) takeEvent() SSEEvent {
	event := r.event
	r.event = SSEEvent{}
	return event
}

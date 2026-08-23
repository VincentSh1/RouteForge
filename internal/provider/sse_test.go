package provider

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestSSEReader(t *testing.T) {
	reader := NewSSEReader(strings.NewReader(": ping\nevent: message\ndata: first\ndata: second\n\ndata: done\n\n"))
	event, err := reader.Next()
	if err != nil || event.Type != "message" || string(event.Data) != "first\nsecond" {
		t.Fatalf("first event = %+v, %v", event, err)
	}
	event, err = reader.Next()
	if err != nil || string(event.Data) != "done" {
		t.Fatalf("second event = %+v, %v", event, err)
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("final error = %v, want EOF", err)
	}
}

func TestSSEReaderAcceptsCRLF(t *testing.T) {
	reader := NewSSEReader(strings.NewReader(": heartbeat\r\ndata: first\r\ndata: second\r\n\r\n"))
	event, err := reader.Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if string(event.Data) != "first\nsecond" {
		t.Fatalf("event data = %q", event.Data)
	}
}

func TestSSEReaderRejectsIncompleteFinalEvent(t *testing.T) {
	reader := NewSSEReader(strings.NewReader("data: incomplete"))
	if _, err := reader.Next(); !errors.Is(err, ErrSSERead) {
		t.Fatalf("Next() error = %v, want ErrSSERead", err)
	}
}

func TestSSEReaderRejectsOversizedLine(t *testing.T) {
	reader := NewSSEReader(strings.NewReader("data: " + strings.Repeat("x", MaxSSEEventSize+1) + "\n\n"))
	if _, err := reader.Next(); err == nil {
		t.Fatal("Next() error = nil")
	}
}

func TestSSEReaderRejectsOversizedMultilineEvent(t *testing.T) {
	part := strings.Repeat("x", MaxSSEEventSize/2)
	reader := NewSSEReader(strings.NewReader("data: " + part + "\ndata: " + part + "\n\n"))
	if _, err := reader.Next(); !errors.Is(err, ErrSSEEventTooLarge) {
		t.Fatalf("Next() error = %v, want ErrSSEEventTooLarge", err)
	}
}

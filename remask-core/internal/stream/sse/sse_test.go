package sse

import (
	"io"
	"strings"
	"testing"
)

func TestDecodeAndEncodeMultilineEvent(t *testing.T) {
	decoder := NewDecoder(strings.NewReader("event: delta\r\nid: 7\r\ndata: first\r\ndata: second\r\n\r\n"))
	event, err := decoder.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if event.Event != "delta" || event.ID != "7" || event.DataString() != "first\nsecond" {
		t.Fatalf("unexpected event: %#v", event)
	}
	if got := string(Encode(event)); got != "event: delta\nid: 7\ndata: first\ndata: second\n\n" {
		t.Fatalf("unexpected encoding: %q", got)
	}
	if _, err := decoder.Decode(); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}
}

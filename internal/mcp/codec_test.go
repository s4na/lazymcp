package mcp

import (
	"bytes"
	"testing"
)

func TestCodecRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	codec := NewCodec(&buf, &buf)
	want := NewResult(float64(1), map[string]any{"ok": true})

	if err := codec.Write(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := codec.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.JSONRPC != "2.0" || got.ID != float64(1) {
		t.Fatalf("unexpected message: %#v", got)
	}
}

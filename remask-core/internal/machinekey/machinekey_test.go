package machinekey

import (
	"bytes"
	"testing"
)

func TestDeriveIsStableAndApplicationScoped(t *testing.T) {
	first := derive("protected-machine-id")
	second := derive("protected-machine-id")
	other := derive("another-protected-machine-id")

	if len(first) != 32 {
		t.Fatalf("derived key length = %d, want 32", len(first))
	}
	if !bytes.Equal(first, second) {
		t.Fatal("same protected machine ID produced different keys")
	}
	if bytes.Equal(first, other) {
		t.Fatal("different protected machine IDs produced the same key")
	}
	if bytes.Equal(first, []byte("protected-machine-id")) {
		t.Fatal("derived key exposes the protected machine ID")
	}
}

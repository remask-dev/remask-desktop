package license

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManagerImportsAtomicallyAndReportsState(t *testing.T) {
	publicKey, privateKey := testKey(t)
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	manager := NewManager(directory, "RMK1-TEST", nil, NewVerifier(map[string]ed25519.PublicKey{"test-v1": publicKey}))
	manager.clock = func() time.Time { return now }
	if state := manager.State(); state.Status != StatusMissing {
		t.Fatalf("initial status = %q", state.Status)
	}
	data, err := Sign(privateKey, "test-v1", testClaims(now))
	if err != nil {
		t.Fatal(err)
	}
	state, err := manager.Import(data)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusValid || state.Edition != "professional" || state.Email != "owner@example.com" {
		t.Fatalf("imported state = %+v", state)
	}
	info, err := os.Stat(filepath.Join(directory, Filename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("license permissions = %o", info.Mode().Perm())
	}
}

func TestInvalidImportDoesNotReplaceExistingLicense(t *testing.T) {
	publicKey, privateKey := testKey(t)
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	manager := NewManager(directory, "RMK1-TEST", nil, NewVerifier(map[string]ed25519.PublicKey{"test-v1": publicKey}))
	manager.clock = func() time.Time { return now }
	valid, _ := Sign(privateKey, "test-v1", testClaims(now))
	if _, err := manager.Import(valid); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Import([]byte("invalid")); err == nil {
		t.Fatal("expected invalid import error")
	}
	stored, err := os.ReadFile(filepath.Join(directory, Filename))
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(valid) {
		t.Fatal("invalid import replaced the existing license")
	}
}

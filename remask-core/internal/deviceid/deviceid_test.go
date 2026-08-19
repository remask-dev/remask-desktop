package deviceid

import "testing"

func TestFormatProtectedID(t *testing.T) {
	got, err := formatProtectedID("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	if err != nil {
		t.Fatal(err)
	}
	const want = "RMK1-AAAQ-EAYE-AUDA-OCAJ-BIFQ-YDIO"
	if got != want {
		t.Fatalf("formatProtectedID() = %q, want %q", got, want)
	}
}

func TestFormatProtectedIDRejectsInvalidInput(t *testing.T) {
	if _, err := formatProtectedID("not-a-digest"); err == nil {
		t.Fatal("expected invalid digest error")
	}
}

package license

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = byte(index + 1)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	return privateKey.Public().(ed25519.PublicKey), privateKey
}

func testClaims(now time.Time) Claims {
	return Claims{
		LicenseID: "lic_test", Product: Product, Edition: "professional", Email: "owner@example.com", DeviceID: "RMK1-TEST",
		IssuedAt: now.Add(-time.Hour), NotBefore: now.Add(-time.Hour), ExpiresAt: now.Add(24 * time.Hour),
		Features: []string{"pii-protection"},
	}
}

func TestSignAndVerify(t *testing.T) {
	publicKey, privateKey := testKey(t)
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	data, err := Sign(privateKey, "test-v1", testClaims(now))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := NewVerifier(map[string]ed25519.PublicKey{"test-v1": publicKey}).Verify(data, "RMK1-TEST", now)
	if err != nil {
		t.Fatal(err)
	}
	if claims.LicenseID != "lic_test" || claims.Email != "owner@example.com" {
		t.Fatalf("license claims = %+v", claims)
	}
}

func TestSignRejectsInvalidOptionalEmail(t *testing.T) {
	_, privateKey := testKey(t)
	claims := testClaims(time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC))
	claims.Email = "not-an-email"
	if _, err := Sign(privateKey, "test-v1", claims); ErrorCode(err) != "LICENSE_CLAIMS_INVALID" {
		t.Fatalf("invalid email error = %v", err)
	}
}

func TestVerifyRejectsTamperingAndDeviceMismatch(t *testing.T) {
	publicKey, privateKey := testKey(t)
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	data, err := Sign(privateKey, "test-v1", testClaims(now))
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewVerifier(map[string]ed25519.PublicKey{"test-v1": publicKey})
	if _, err := verifier.Verify(data, "RMK1-OTHER", now); ErrorCode(err) != "LICENSE_DEVICE_MISMATCH" {
		t.Fatalf("device mismatch error = %v", err)
	}

	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
	if err != nil {
		t.Fatal(err)
	}
	payload[0] ^= 1
	envelope.Payload = base64.RawURLEncoding.EncodeToString(payload)
	tampered, _ := json.Marshal(envelope)
	if _, err := verifier.Verify(tampered, "RMK1-TEST", now); ErrorCode(err) != "LICENSE_SIGNATURE_INVALID" {
		t.Fatalf("tampering error = %v", err)
	}
}

func TestVerifyRejectsExpiredLicense(t *testing.T) {
	publicKey, privateKey := testKey(t)
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	claims := testClaims(now)
	claims.ExpiresAt = now
	data, err := Sign(privateKey, "test-v1", claims)
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewVerifier(map[string]ed25519.PublicKey{"test-v1": publicKey}).Verify(data, "RMK1-TEST", now)
	if ErrorCode(err) != "LICENSE_EXPIRED" {
		t.Fatalf("expiry error = %v", err)
	}
}

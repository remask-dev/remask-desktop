package license

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/mail"
	"strings"
	"time"
)

const signatureDomain = "REMASK-LICENSE\x00v1\x00"

type Verifier struct {
	publicKeys map[string]ed25519.PublicKey
}

func NewVerifier(publicKeys map[string]ed25519.PublicKey) *Verifier {
	keys := make(map[string]ed25519.PublicKey, len(publicKeys))
	for id, key := range publicKeys {
		keys[id] = append(ed25519.PublicKey(nil), key...)
	}
	return &Verifier{publicKeys: keys}
}

// ParsePublicKey accepts standard or raw Base64 Ed25519 public keys.
func ParsePublicKey(encoded string) (ed25519.PublicKey, error) {
	encoded = strings.TrimSpace(encoded)
	var decoded []byte
	var err error
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		decoded, err = encoding.DecodeString(encoded)
		if err == nil {
			break
		}
	}
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key must be a Base64-encoded Ed25519 key")
	}
	return ed25519.PublicKey(decoded), nil
}

func (v *Verifier) Verify(data []byte, deviceID string, now time.Time) (Claims, error) {
	if len(data) == 0 || len(data) > maxLicenseBytes {
		return Claims{}, &codedError{code: "LICENSE_FILE_INVALID", message: "license file must be between 1 byte and 64 KiB"}
	}
	var envelope Envelope
	if err := decodeStrict(data, &envelope); err != nil {
		return Claims{}, &codedError{code: "LICENSE_FILE_INVALID", message: "license file is not valid JSON"}
	}
	if envelope.Format != Format || envelope.Version != Version {
		return Claims{}, &codedError{code: "LICENSE_FORMAT_UNSUPPORTED", message: "license file format or version is unsupported"}
	}
	key, ok := v.publicKeys[envelope.KeyID]
	if !ok {
		return Claims{}, &codedError{code: "LICENSE_KEY_UNKNOWN", message: "license signing key is not configured"}
	}
	payload, err := base64.RawURLEncoding.DecodeString(envelope.Payload)
	if err != nil {
		return Claims{}, &codedError{code: "LICENSE_FILE_INVALID", message: "license payload encoding is invalid"}
	}
	signature, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return Claims{}, &codedError{code: "LICENSE_SIGNATURE_INVALID", message: "license signature encoding is invalid"}
	}
	if !ed25519.Verify(key, append([]byte(signatureDomain), payload...), signature) {
		return Claims{}, &codedError{code: "LICENSE_SIGNATURE_INVALID", message: "license signature is invalid"}
	}
	var claims Claims
	if err := decodeStrict(payload, &claims); err != nil {
		return Claims{}, &codedError{code: "LICENSE_CLAIMS_INVALID", message: "license claims are invalid"}
	}
	if err := validateClaims(claims, deviceID, now.UTC()); err != nil {
		return claims, err
	}
	return claims, nil
}

func validateClaims(claims Claims, deviceID string, now time.Time) error {
	if claims.LicenseID == "" || len(claims.LicenseID) > 128 || claims.Product != Product || claims.Edition == "" || len(claims.Edition) > 64 || len(claims.DeviceID) > 128 {
		return &codedError{code: "LICENSE_CLAIMS_INVALID", message: "required license claims are missing or invalid"}
	}
	if claims.Email != "" {
		address, err := mail.ParseAddress(claims.Email)
		if err != nil || address.Address != claims.Email || len(claims.Email) > 254 {
			return &codedError{code: "LICENSE_CLAIMS_INVALID", message: "license email is invalid"}
		}
	}
	if claims.IssuedAt.IsZero() || claims.NotBefore.IsZero() || claims.ExpiresAt.IsZero() || claims.ExpiresAt.Before(claims.NotBefore) || claims.NotBefore.Before(claims.IssuedAt.Add(-5*time.Minute)) {
		return &codedError{code: "LICENSE_CLAIMS_INVALID", message: "license validity period is invalid"}
	}
	if len(claims.Features) > 64 {
		return &codedError{code: "LICENSE_CLAIMS_INVALID", message: "license contains too many features"}
	}
	for _, feature := range claims.Features {
		if feature == "" || len(feature) > 64 {
			return &codedError{code: "LICENSE_CLAIMS_INVALID", message: "license contains an invalid feature"}
		}
	}
	if claims.DeviceID != deviceID {
		return &codedError{code: "LICENSE_DEVICE_MISMATCH", message: "license belongs to another device"}
	}
	if now.Before(claims.NotBefore) {
		return &codedError{code: "LICENSE_NOT_YET_VALID", message: "license is not active yet"}
	}
	if !now.Before(claims.ExpiresAt) {
		return &codedError{code: "LICENSE_EXPIRED", message: "license has expired"}
	}
	return nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("unexpected trailing data")
	}
	return nil
}

func Sign(privateKey ed25519.PrivateKey, keyID string, claims Claims) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize || strings.TrimSpace(keyID) == "" {
		return nil, fmt.Errorf("private key and key ID are required")
	}
	if err := validateClaims(claims, claims.DeviceID, claims.NotBefore); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return nil, err
	}
	signature := ed25519.Sign(privateKey, append([]byte(signatureDomain), payload...))
	envelope := Envelope{
		Format: Format, Version: Version, KeyID: keyID,
		Payload:   base64.RawURLEncoding.EncodeToString(payload),
		Signature: base64.RawURLEncoding.EncodeToString(signature),
	}
	return json.MarshalIndent(envelope, "", "  ")
}

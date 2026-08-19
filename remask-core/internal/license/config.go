package license

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"strings"
)

const (
	DefaultKeyID        = "prod-v1"
	RepositoryPublicKey = "iyv5hSlaLZ5juGcA6uuNnGf2jLeU2yrjnqqYAENwUBI="
)

// EmbeddedPublicKey defaults to the production verification key kept in this
// private repository. It may be replaced at build time during key rotation:
//
//	-ldflags "-X github.com/remask/remask-core/internal/license.EmbeddedPublicKey=<base64>"
//
// Only the public verification key belongs in the client. The corresponding
// private signing key must never be committed or included in a build.
var EmbeddedPublicKey = RepositoryPublicKey
var EmbeddedKeyID = DefaultKeyID

func ConfiguredVerifier() (*Verifier, error) {
	// A release-embedded key is authoritative. Environment configuration is
	// only a development fallback; it must never let a launched production
	// binary replace its trust root.
	encoded := strings.TrimSpace(EmbeddedPublicKey)
	keyID := strings.TrimSpace(EmbeddedKeyID)
	if encoded == "" {
		encoded = strings.TrimSpace(os.Getenv("REMASK_LICENSE_PUBLIC_KEY"))
		keyID = strings.TrimSpace(os.Getenv("REMASK_LICENSE_KEY_ID"))
	}
	if encoded == "" {
		return NewVerifier(nil), nil
	}
	key, err := ParsePublicKey(encoded)
	if err != nil {
		return nil, fmt.Errorf("configure license public key: %w", err)
	}
	if keyID == "" {
		keyID = DefaultKeyID
	}
	return NewVerifier(map[string]ed25519.PublicKey{keyID: key}), nil
}

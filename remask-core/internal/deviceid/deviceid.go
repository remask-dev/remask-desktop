// Package deviceid derives a stable, application-scoped identifier from the
// operating system's installation identifier. The underlying machine ID must
// never leave this package or be exposed through the management API.
package deviceid

import (
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/keygen-sh/machineid"
)

const namespace = "com.remask/device/v1"

// ID returns a stable Remask device ID without exposing the OS machine ID.
func ID() (string, error) {
	protected, err := machineid.ProtectedID(namespace)
	if err != nil {
		return "", fmt.Errorf("read protected machine id: %w", err)
	}
	return formatProtectedID(protected)
}

func formatProtectedID(protected string) (string, error) {
	digest, err := hex.DecodeString(protected)
	if err != nil || len(digest) < 15 {
		return "", fmt.Errorf("invalid protected machine id")
	}
	// 120 bits gives 24 Base32 characters: enough collision resistance for a
	// licensing identifier while remaining practical to copy by hand.
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:15])
	groups := make([]string, 0, 6)
	for len(encoded) > 0 {
		groups = append(groups, encoded[:4])
		encoded = encoded[4:]
	}
	return "RMK1-" + strings.Join(groups, "-"), nil
}

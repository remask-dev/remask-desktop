package machinekey

import (
	"crypto/sha256"
	"fmt"

	"github.com/denisbrodbeck/machineid"
)

const applicationID = "remask"

// Derive resolves an application-specific machine identifier and derives the
// fixed-size key used for deterministic PII labels. The machine identifier is
// never persisted by Remask.
func Derive() ([]byte, error) {
	id, err := machineid.ProtectedID(applicationID)
	if err != nil {
		return nil, fmt.Errorf("resolve protected machine ID: %w", err)
	}
	return derive(id), nil
}

func derive(protectedID string) []byte {
	sum := sha256.Sum256([]byte("remask-device-key-v1\x00" + protectedID))
	return sum[:]
}

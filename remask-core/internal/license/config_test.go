package license

import "testing"

func TestRepositoryPublicKeyConfiguresVerifier(t *testing.T) {
	if _, err := ConfiguredVerifier(); err != nil {
		t.Fatalf("configure repository public key: %v", err)
	}
}

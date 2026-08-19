package license

import "time"

const (
	Format          = "remask-license"
	Version         = 1
	Product         = "remask-desktop"
	Filename        = "remask.license"
	maxLicenseBytes = 64 << 10
)

const (
	StatusMissing           = "missing"
	StatusValid             = "valid"
	StatusExpired           = "expired"
	StatusNotYetValid       = "not_yet_valid"
	StatusDeviceMismatch    = "device_mismatch"
	StatusInvalid           = "invalid"
	StatusKeyUnconfigured   = "key_unconfigured"
	StatusDeviceUnavailable = "device_unavailable"
)

// Envelope is the stable on-disk .license container. Payload is Base64URL
// encoded JSON so signatures do not depend on a JSON serializer's formatting.
type Envelope struct {
	Format    string `json:"format"`
	Version   int    `json:"version"`
	KeyID     string `json:"key_id"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

type Claims struct {
	LicenseID string    `json:"license_id"`
	Product   string    `json:"product"`
	Edition   string    `json:"edition"`
	Email     string    `json:"email,omitempty"`
	DeviceID  string    `json:"device_id"`
	IssuedAt  time.Time `json:"issued_at"`
	NotBefore time.Time `json:"not_before"`
	ExpiresAt time.Time `json:"expires_at"`
	Features  []string  `json:"features,omitempty"`
	OrderRef  string    `json:"order_ref,omitempty"`
}

// State is safe to expose through the loopback management API. It never
// contains the OS machine identifier or the signed payload.
type State struct {
	DeviceID  string     `json:"device_id"`
	Status    string     `json:"status"`
	LicenseID string     `json:"license_id,omitempty"`
	Edition   string     `json:"edition,omitempty"`
	Email     string     `json:"email,omitempty"`
	IssuedAt  *time.Time `json:"issued_at,omitempty"`
	NotBefore *time.Time `json:"not_before,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Features  []string   `json:"features,omitempty"`
	Code      string     `json:"code,omitempty"`
}

type codedError struct {
	code    string
	message string
}

func (e *codedError) Error() string { return e.message }

func ErrorCode(err error) string {
	if value, ok := err.(*codedError); ok {
		return value.code
	}
	return "LICENSE_INVALID"
}

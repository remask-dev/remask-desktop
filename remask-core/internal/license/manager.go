package license

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Manager struct {
	mu        sync.RWMutex
	path      string
	deviceID  string
	deviceErr error
	verifier  *Verifier
	clock     func() time.Time
	data      []byte
	loadErr   error
}

func NewManager(dataDir, deviceID string, deviceErr error, verifier *Verifier) *Manager {
	if verifier == nil {
		verifier = NewVerifier(nil)
	}
	path := ""
	if dataDir != "" {
		path = filepath.Join(dataDir, Filename)
	}
	manager := &Manager{
		path: path, deviceID: deviceID, deviceErr: deviceErr,
		verifier: verifier, clock: time.Now,
	}
	manager.load()
	return manager
}

func (m *Manager) load() {
	if m.path == "" {
		return
	}
	data, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		m.loadErr = err
		return
	}
	m.data = data
}

func (m *Manager) State() State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.stateLocked()
}

func (m *Manager) stateLocked() State {
	state := State{DeviceID: m.deviceID, Status: StatusMissing}
	if m.deviceErr != nil {
		state.Status, state.Code = StatusDeviceUnavailable, "DEVICE_ID_UNAVAILABLE"
		return state
	}
	if m.loadErr != nil {
		state.Status, state.Code = StatusInvalid, "LICENSE_READ_FAILED"
		return state
	}
	if len(m.data) == 0 {
		return state
	}
	claims, err := m.verifier.Verify(m.data, m.deviceID, m.clock().UTC())
	if err != nil {
		if claims.LicenseID != "" {
			state = stateFromClaims(m.deviceID, claims)
		}
		state.Code = ErrorCode(err)
		switch state.Code {
		case "LICENSE_EXPIRED":
			state.Status = StatusExpired
		case "LICENSE_NOT_YET_VALID":
			state.Status = StatusNotYetValid
		case "LICENSE_DEVICE_MISMATCH":
			state.Status = StatusDeviceMismatch
		case "LICENSE_KEY_UNKNOWN":
			state.Status = StatusKeyUnconfigured
		default:
			state.Status = StatusInvalid
		}
		return state
	}
	return stateFromClaims(m.deviceID, claims)
}

func stateFromClaims(deviceID string, claims Claims) State {
	issuedAt, notBefore, expiresAt := claims.IssuedAt, claims.NotBefore, claims.ExpiresAt
	return State{
		DeviceID: deviceID, Status: StatusValid, LicenseID: claims.LicenseID, Edition: claims.Edition,
		Email:    claims.Email,
		IssuedAt: &issuedAt, NotBefore: &notBefore, ExpiresAt: &expiresAt,
		Features: append([]string(nil), claims.Features...),
	}
}

// Import validates before writing, so an invalid file can never replace a
// currently working license.
func (m *Manager) Import(data []byte) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deviceErr != nil {
		return State{}, fmt.Errorf("resolve device id: %w", m.deviceErr)
	}
	if m.path == "" {
		return State{}, fmt.Errorf("license data directory is not configured")
	}
	claims, err := m.verifier.Verify(data, m.deviceID, m.clock().UTC())
	if err != nil {
		return State{}, err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return State{}, fmt.Errorf("create license directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(m.path), ".remask-license-*")
	if err != nil {
		return State{}, fmt.Errorf("create temporary license: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return State{}, fmt.Errorf("secure temporary license: %w", err)
	}
	_, err = temporary.Write(data)
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return State{}, fmt.Errorf("write license file: %w", err)
	}
	if err := replaceFile(temporaryPath, m.path); err != nil {
		return State{}, fmt.Errorf("install license file: %w", err)
	}
	m.data = append(m.data[:0], data...)
	m.loadErr = nil
	return stateFromClaims(m.deviceID, claims), nil
}

package scope

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("scope not found")

type Vault interface {
	ID() string
	TokenFor(entityType, original string) (string, error)
	Resolve(token string) (string, bool)
	ExpiresAt() time.Time
}

type Store interface {
	Create(ctx context.Context, ttl time.Duration) (Vault, error)
	Get(ctx context.Context, id string) (Vault, error)
	Delete(ctx context.Context, id string) error
}

type memoryVault struct {
	mu        sync.RWMutex
	id        string
	expiresAt time.Time
	byValue   map[string]string
	byToken   map[string]string
}

func (v *memoryVault) ID() string           { return v.id }
func (v *memoryVault) ExpiresAt() time.Time { return v.expiresAt }

func (v *memoryVault) TokenFor(entityType, original string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	key := entityType + "\x00" + original
	if token, ok := v.byValue[key]; ok {
		return token, nil
	}

	for attempts := 0; attempts < 32; attempts++ {
		suffix := deterministicSuffix(entityType, original, attempts)
		token := fmt.Sprintf("<%s:%s>", maskIdentifier(entityType), suffix)
		if strings.Contains(original, token) {
			continue
		}
		if _, exists := v.byToken[token]; exists {
			continue
		}
		v.byValue[key] = token
		v.byToken[token] = original
		return token, nil
	}
	return "", errors.New("unable to generate unique replacement token")
}

func maskIdentifier(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	var result strings.Builder
	for _, character := range value {
		if character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			result.WriteRune(character)
		} else if result.Len() > 0 && !strings.HasSuffix(result.String(), "_") {
			result.WriteByte('_')
		}
	}
	normalized := strings.Trim(result.String(), "_")
	if normalized == "" {
		return "ENTITY"
	}
	if normalized[0] >= '0' && normalized[0] <= '9' {
		return "RULE_" + normalized
	}
	return normalized
}

func (v *memoryVault) Resolve(token string) (string, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	original, ok := v.byToken[token]
	return original, ok
}

type MemoryStore struct {
	mu         sync.RWMutex
	defaultTTL time.Duration
	vaults     map[string]*memoryVault
}

func NewMemoryStore(defaultTTL time.Duration) (*MemoryStore, error) {
	return &MemoryStore{defaultTTL: defaultTTL, vaults: make(map[string]*memoryVault)}, nil
}

func (s *MemoryStore) Create(ctx context.Context, ttl time.Duration) (Vault, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if ttl <= 0 {
		ttl = s.defaultTTL
	}
	idPart, err := randomHex(12)
	if err != nil {
		return nil, err
	}
	vault := &memoryVault{
		id:        "scp_" + strings.ToLower(idPart),
		expiresAt: time.Now().UTC().Add(ttl),
		byValue:   make(map[string]string),
		byToken:   make(map[string]string),
	}
	s.mu.Lock()
	s.vaults[vault.id] = vault
	s.mu.Unlock()
	return vault, nil
}

func (s *MemoryStore) Get(ctx context.Context, id string) (Vault, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	vault, ok := s.vaults[id]
	s.mu.RUnlock()
	if !ok {
		return nil, ErrNotFound
	}
	if time.Now().After(vault.expiresAt) {
		_ = s.Delete(ctx, id)
		return nil, ErrNotFound
	}
	return vault, nil
}

func (s *MemoryStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.vaults, id)
	s.mu.Unlock()
	return nil
}

func randomHex(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(buffer)), nil
}

func deterministicSuffix(entityType, original string, attempt int) string {
	// Keep the original HMAC input and domain prefix; only the device-derived
	// key has been removed so suffixes are independent of device identity.
	mac := hmac.New(sha256.New, nil)
	_, _ = mac.Write([]byte("remask-token-v1\x00"))
	_, _ = mac.Write([]byte(entityType))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(original))
	_, _ = mac.Write([]byte{0, byte(attempt)})
	return strings.ToUpper(hex.EncodeToString(mac.Sum(nil)[:2]))
}

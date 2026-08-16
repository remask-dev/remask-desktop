package pii

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultEntityCacheTTL is deliberately short enough to avoid retaining
	// detector results after a policy/model change, while still covering the
	// common repeated-request window.
	DefaultEntityCacheTTL        = 15 * time.Minute
	DefaultEntityCacheMaxEntries = 4096
	minimumEntityCacheTTL        = 10 * time.Millisecond
	maximumEntityCacheTTL        = 24 * time.Hour

	EntityCacheBackendMemory = "memory"
	EntityCacheBackendRedis  = "redis"
)

// EntityCacheConfig controls the cache backend. Backend and RedisURL are
// process-level settings; Enabled, TTL and MaxEntries can be changed at run
// time through the settings API.
type EntityCacheConfig struct {
	Enabled    bool
	TTL        time.Duration
	MaxEntries int
	Backend    string
	RedisURL   string
	KeyPrefix  string
}

func DefaultEntityCacheConfig() EntityCacheConfig {
	return EntityCacheConfig{
		Enabled:    true,
		TTL:        DefaultEntityCacheTTL,
		MaxEntries: DefaultEntityCacheMaxEntries,
		Backend:    EntityCacheBackendMemory,
	}
}

func (c EntityCacheConfig) normalize() (EntityCacheConfig, error) {
	if c.TTL == 0 {
		c.TTL = DefaultEntityCacheTTL
	}
	if c.TTL < minimumEntityCacheTTL || c.TTL > maximumEntityCacheTTL {
		return c, errors.New("entity cache ttl must be between 10 milliseconds and 24 hours")
	}
	if c.MaxEntries <= 0 {
		c.MaxEntries = DefaultEntityCacheMaxEntries
	}
	if c.MaxEntries > 100000 {
		return c, errors.New("entity cache max entries must be at most 100000")
	}
	c.Backend = strings.ToLower(strings.TrimSpace(c.Backend))
	if c.Backend == "" {
		c.Backend = EntityCacheBackendMemory
	}
	if c.Backend != EntityCacheBackendMemory && c.Backend != EntityCacheBackendRedis {
		return c, fmt.Errorf("unsupported entity cache backend %q", c.Backend)
	}
	if c.Backend == EntityCacheBackendRedis && strings.TrimSpace(c.RedisURL) == "" {
		return c, errors.New("redis entity cache requires a redis url")
	}
	if c.KeyPrefix == "" {
		c.KeyPrefix = "remask:pii:entity:v1:"
	}
	return c, nil
}

// EntityCacheBackend is deliberately small so the detector service does not
// depend on a particular cache implementation. Implementations must treat
// cache failures as recoverable: callers can fall back to detection.
type EntityCacheBackend interface {
	Lookup(ctx context.Context, text string) ([]Entity, bool, error)
	Store(ctx context.Context, text string, entities []Entity) error
	Configure(config EntityCacheConfig) error
	Config() EntityCacheConfig
	Clear(ctx context.Context) error
	Close() error
}

func NewEntityCache(config EntityCacheConfig) (*MemoryEntityCache, error) {
	config.Backend = EntityCacheBackendMemory
	return NewMemoryEntityCache(config)
}

func NewEntityCacheBackend(config EntityCacheConfig) (EntityCacheBackend, error) {
	config, err := config.normalize()
	if err != nil {
		return nil, err
	}
	switch config.Backend {
	case EntityCacheBackendMemory:
		return NewMemoryEntityCache(config)
	case EntityCacheBackendRedis:
		return NewRedisEntityCache(config)
	default:
		return nil, fmt.Errorf("unsupported entity cache backend %q", config.Backend)
	}
}

func cacheKey(text string, prefix string) string {
	digest := sha256.Sum256([]byte(text))
	return prefix + hex.EncodeToString(digest[:])
}

func cloneEntities(source []Entity) []Entity {
	if source == nil {
		return nil
	}
	result := make([]Entity, len(source))
	for i, entity := range source {
		result[i] = entity
		result[i].Sources = append([]string(nil), entity.Sources...)
	}
	return result
}

func cacheableEntities(source []Entity) []Entity {
	result := cloneEntities(source)
	for index := range result {
		result[index].Text = ""
		result[index].Replacement = ""
	}
	return result
}

func hydrateCachedEntities(text string, source []Entity) []Entity {
	result := cloneEntities(source)
	for index := range result {
		entity := &result[index]
		if entity.StartByte >= 0 && entity.StartByte < entity.EndByte && entity.EndByte <= len(text) {
			entity.Text = text[entity.StartByte:entity.EndByte]
		}
		entity.Replacement = ""
	}
	return result
}

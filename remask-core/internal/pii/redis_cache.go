package pii

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisEntityCache provides the same backend contract for multi-process or
// multi-instance deployments. GETEX makes sliding expiration atomic on Redis
// 6.2 and newer.
type RedisEntityCache struct {
	client           redis.UniversalClient
	mu               sync.RWMutex
	config           EntityCacheConfig
	unavailableUntil time.Time
}

func NewRedisEntityCache(config EntityCacheConfig) (*RedisEntityCache, error) {
	config.Backend = EntityCacheBackendRedis
	config, err := config.normalize()
	if err != nil {
		return nil, err
	}
	options, err := redis.ParseURL(strings.TrimSpace(config.RedisURL))
	if err != nil {
		return nil, err
	}
	// Cache calls sit on the latency-sensitive detection path. Fail quickly and
	// let local detection continue when Redis is unavailable.
	options.DialTimeout = 500 * time.Millisecond
	options.ReadTimeout = 500 * time.Millisecond
	options.WriteTimeout = 500 * time.Millisecond
	options.PoolTimeout = 500 * time.Millisecond
	options.MaxRetries = -1
	client := redis.NewClient(options)
	return &RedisEntityCache{client: client, config: config}, nil
}

func NewRedisEntityCacheWithClient(client redis.UniversalClient, config EntityCacheConfig) (*RedisEntityCache, error) {
	if client == nil {
		return nil, redis.Nil
	}
	if config.RedisURL == "" {
		config.RedisURL = "redis://injected-client"
	}
	config.Backend = EntityCacheBackendRedis
	config, err := config.normalize()
	if err != nil {
		return nil, err
	}
	return &RedisEntityCache{client: client, config: config}, nil
}

func (c *RedisEntityCache) Lookup(ctx context.Context, text string) ([]Entity, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	c.mu.RLock()
	configured := c.config
	c.mu.RUnlock()
	if !configured.Enabled {
		return nil, false, nil
	}
	if err := c.backendAvailable(); err != nil {
		return nil, false, err
	}
	key := cacheKey(text, configured.KeyPrefix)
	value, err := c.client.GetEx(ctx, key, configured.TTL).Bytes()
	if err == redis.Nil {
		c.markAvailable()
		return nil, false, nil
	}
	if err != nil {
		c.markUnavailable()
		return nil, false, err
	}
	c.markAvailable()
	var entities []Entity
	if err := json.Unmarshal(value, &entities); err != nil {
		_ = c.client.Del(ctx, key).Err()
		return nil, false, err
	}
	return hydrateCachedEntities(text, entities), true, nil
}

func (c *RedisEntityCache) Store(ctx context.Context, text string, entities []Entity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.RLock()
	configured := c.config
	c.mu.RUnlock()
	if !configured.Enabled {
		return nil
	}
	if err := c.backendAvailable(); err != nil {
		return err
	}
	value, err := json.Marshal(cacheableEntities(entities))
	if err != nil {
		return err
	}
	if err := c.client.Set(ctx, cacheKey(text, configured.KeyPrefix), value, configured.TTL).Err(); err != nil {
		c.markUnavailable()
		return err
	}
	c.markAvailable()
	return nil
}

func (c *RedisEntityCache) Configure(config EntityCacheConfig) error {
	c.mu.RLock()
	current := c.config
	c.mu.RUnlock()
	config.Backend = EntityCacheBackendRedis
	config.RedisURL = current.RedisURL
	config.KeyPrefix = current.KeyPrefix
	config, err := config.normalize()
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.config = config
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// Configuration is local state and remains valid even if Redis is
	// temporarily unavailable. Old entries retain their own TTL and will be
	// ignored while the cache is disabled.
	_ = c.Clear(ctx)
	return nil
}

func (c *RedisEntityCache) Config() EntityCacheConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config
}

func (c *RedisEntityCache) Clear(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.backendAvailable(); err != nil {
		return err
	}
	var cursor uint64
	c.mu.RLock()
	pattern := c.config.KeyPrefix + "*"
	c.mu.RUnlock()
	for {
		keys, next, err := c.client.Scan(ctx, cursor, pattern, 256).Result()
		if err != nil {
			c.markUnavailable()
			return err
		}
		if len(keys) > 0 {
			if err := c.client.Del(ctx, keys...).Err(); err != nil {
				c.markUnavailable()
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			c.markAvailable()
			return nil
		}
	}
}

func (c *RedisEntityCache) Close() error { return c.client.Close() }

func (c *RedisEntityCache) backendAvailable() error {
	c.mu.RLock()
	unavailableUntil := c.unavailableUntil
	c.mu.RUnlock()
	if time.Now().Before(unavailableUntil) {
		return errors.New("redis entity cache is temporarily unavailable")
	}
	return nil
}

func (c *RedisEntityCache) markUnavailable() {
	c.mu.Lock()
	c.unavailableUntil = time.Now().Add(5 * time.Second)
	c.mu.Unlock()
}

func (c *RedisEntityCache) markAvailable() {
	c.mu.Lock()
	c.unavailableUntil = time.Time{}
	c.mu.Unlock()
}

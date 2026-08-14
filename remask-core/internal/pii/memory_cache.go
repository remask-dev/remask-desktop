package pii

import (
	"context"
	"sync"
	"time"

	cache "github.com/patrickmn/go-cache"
)

// MemoryEntityCache uses the battle-tested go-cache janitor/expiration
// implementation while keeping entity-specific hashing, cloning, and bounded
// eviction in this package.
type MemoryEntityCache struct {
	mu     sync.RWMutex
	cache  *cache.Cache
	config EntityCacheConfig
}

func NewMemoryEntityCache(config EntityCacheConfig) (*MemoryEntityCache, error) {
	config.Backend = EntityCacheBackendMemory
	config, err := config.normalize()
	if err != nil {
		return nil, err
	}
	return &MemoryEntityCache{cache: newMemoryCache(config.TTL), config: config}, nil
}

func (c *MemoryEntityCache) Lookup(ctx context.Context, text string) ([]Entity, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	configured := c.config
	itemCache := c.cache
	if !configured.Enabled {
		return nil, false, nil
	}
	key := cacheKey(text, configured.KeyPrefix)
	value, ok := itemCache.Get(key)
	if !ok {
		return nil, false, nil
	}
	entities, ok := value.([]Entity)
	if !ok {
		itemCache.Delete(key)
		return nil, false, nil
	}
	// go-cache does not provide sliding expiration, so replace the value on
	// every hit with the configured TTL.
	itemCache.Set(key, cacheableEntities(entities), configured.TTL)
	return hydrateCachedEntities(text, entities), true, nil
}

func (c *MemoryEntityCache) Store(ctx context.Context, text string, entities []Entity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	configured := c.config
	itemCache := c.cache
	if !configured.Enabled {
		return nil
	}
	key := cacheKey(text, configured.KeyPrefix)
	if _, exists := itemCache.Get(key); !exists && len(itemCache.Items()) >= configured.MaxEntries {
		evictOldest(itemCache)
	}
	itemCache.Set(key, cacheableEntities(entities), configured.TTL)
	return nil
}

func (c *MemoryEntityCache) Configure(config EntityCacheConfig) error {
	c.mu.Lock()
	config.Backend = EntityCacheBackendMemory
	config.KeyPrefix = c.config.KeyPrefix
	config, err := config.normalize()
	if err != nil {
		c.mu.Unlock()
		return err
	}
	c.config = config
	itemCache := c.cache
	c.mu.Unlock()
	itemCache.Flush()
	return nil
}

func (c *MemoryEntityCache) Config() EntityCacheConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config
}

func (c *MemoryEntityCache) Clear(_ context.Context) error {
	c.mu.RLock()
	itemCache := c.cache
	c.mu.RUnlock()
	itemCache.Flush()
	return nil
}

func (c *MemoryEntityCache) Close() error {
	c.mu.Lock()
	itemCache := c.cache
	c.cache = cache.New(0, 0)
	c.mu.Unlock()
	itemCache.Flush()
	return nil
}

// Get/Set/Len preserve the small test/debug API that existed before the
// backend abstraction. Production code uses EntityCacheBackend.
func (c *MemoryEntityCache) Get(ctx context.Context, text string) ([]Entity, bool) {
	entities, ok, _ := c.Lookup(ctx, text)
	return entities, ok
}

func (c *MemoryEntityCache) Set(ctx context.Context, text string, entities []Entity) {
	_ = c.Store(ctx, text, entities)
}

func (c *MemoryEntityCache) Len() int {
	c.mu.RLock()
	itemCache := c.cache
	c.mu.RUnlock()
	return len(itemCache.Items())
}

func newMemoryCache(ttl time.Duration) *cache.Cache {
	return cache.New(ttl, cleanupInterval(ttl))
}

func cleanupInterval(ttl time.Duration) time.Duration {
	interval := ttl / 2
	if interval < 10*time.Millisecond {
		interval = 10 * time.Millisecond
	}
	if interval > time.Minute {
		interval = time.Minute
	}
	return interval
}

func evictOldest(itemCache *cache.Cache) {
	items := itemCache.Items()
	var oldestKey string
	var oldestExpiration int64
	for key, item := range items {
		if item.Expiration == 0 {
			continue
		}
		if oldestKey == "" || item.Expiration < oldestExpiration {
			oldestKey, oldestExpiration = key, item.Expiration
		}
	}
	if oldestKey != "" {
		itemCache.Delete(oldestKey)
	}
}

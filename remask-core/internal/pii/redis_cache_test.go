package pii

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisEntityCacheStoresHashAndRefreshesTTL(t *testing.T) {
	server, err := miniredis.Run()
	if err != nil {
		t.Skipf("local TCP listeners are unavailable: %v", err)
	}
	t.Cleanup(server.Close)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	config := EntityCacheConfig{
		Enabled: true, TTL: time.Minute, Backend: EntityCacheBackendRedis,
		RedisURL: "redis://" + server.Addr(), KeyPrefix: "test:entities:",
	}
	cache, err := NewRedisEntityCacheWithClient(client, config)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()

	text := "contact foo@example.com"
	entities := []Entity{{Type: "EMAIL", Text: "foo@example.com", StartByte: 8, EndByte: 23, Sources: []string{"test"}}}
	if err := cache.Store(context.Background(), text, entities); err != nil {
		t.Fatal(err)
	}
	key := cacheKey(text, config.KeyPrefix)
	if !server.Exists(key) || strings.Contains(key, text) {
		t.Fatalf("redis cache key must contain only the namespaced hash: %q", key)
	}
	stored, err := server.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "foo@example.com") || strings.Contains(stored, text) {
		t.Fatalf("redis cache value contains original text: %s", stored)
	}

	server.FastForward(40 * time.Second)
	got, ok, err := cache.Lookup(context.Background(), text)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || len(got) != 1 || got[0].Type != "EMAIL" || got[0].Text != "foo@example.com" {
		t.Fatalf("unexpected redis cache result: ok=%v entities=%#v", ok, got)
	}
	if ttl := server.TTL(key); ttl != time.Minute {
		t.Fatalf("redis cache hit did not refresh ttl: %v", ttl)
	}
}

func TestRedisEntityCacheClearUsesNamespace(t *testing.T) {
	server, err := miniredis.Run()
	if err != nil {
		t.Skipf("local TCP listeners are unavailable: %v", err)
	}
	t.Cleanup(server.Close)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	config := EntityCacheConfig{
		Enabled: true, TTL: time.Minute, Backend: EntityCacheBackendRedis,
		RedisURL: "redis://" + server.Addr(), KeyPrefix: "test:entities:",
	}
	cache, err := NewRedisEntityCacheWithClient(client, config)
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	if err := cache.Store(context.Background(), "sensitive", []Entity{{Type: "SECRET"}}); err != nil {
		t.Fatal(err)
	}
	server.Set("other:application:key", "keep")
	if err := cache.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	if server.Exists(cacheKey("sensitive", config.KeyPrefix)) || !server.Exists("other:application:key") {
		t.Fatal("redis cache clear escaped its configured namespace")
	}
}

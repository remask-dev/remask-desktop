package pii

import (
	"bytes"
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/remask-dev/remask-core/internal/scope"
)

func TestRedactReturnsTextAndEntitiesThenRestores(t *testing.T) {
	service := newTestService(t)
	input := "请联系 sk-test-1234567890123456"

	result, err := service.Redact(context.Background(), input, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.ReplacementCount != 1 || len(result.Entities) != 1 {
		t.Fatalf("expected one entity, got %#v", result)
	}
	if result.Text == input {
		t.Fatal("expected redacted text")
	}
	for _, entity := range result.Entities {
		if !regexp.MustCompile(`^<MASK_SECRET_KEY:[A-F0-9]{4}>$`).MatchString(entity.Replacement) {
			t.Fatalf("invalid replacement %q", entity.Replacement)
		}
		if entity.Text != "" {
			t.Fatal("redact response must not repeat original entity text")
		}
	}

	restored, err := service.Restore(context.Background(), result.ScopeID, result.Text)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Text != input || restored.RestoredCount != 1 {
		t.Fatalf("unexpected restore result: %#v", restored)
	}
}

func TestSameValueReusesTokenWithinScope(t *testing.T) {
	service := newTestService(t)
	first, err := service.Redact(context.Background(), "sk-test-1234567890123456", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Redact(context.Background(), "再次联系 sk-test-1234567890123456", first.ScopeID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Entities[0].Replacement != second.Entities[0].Replacement {
		t.Fatalf("expected stable token, got %s and %s", first.Entities[0].Replacement, second.Entities[0].Replacement)
	}
}

func TestSameValueReusesTokenAcrossRequestScopes(t *testing.T) {
	service := newTestService(t)
	first, err := service.Redact(context.Background(), "sk-test-1234567890123456", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Redact(context.Background(), "sk-test-1234567890123456", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.ScopeID == second.ScopeID {
		t.Fatal("expected independent request scopes")
	}
	if first.Entities[0].Replacement != second.Entities[0].Replacement {
		t.Fatalf("expected deterministic token, got %s and %s", first.Entities[0].Replacement, second.Entities[0].Replacement)
	}
}

func TestSameValueReusesTokenAcrossStoreRestart(t *testing.T) {
	firstStore, err := scope.NewMemoryStore(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := scope.NewMemoryStore(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewService(NewRuleDetector(), firstStore).Redact(context.Background(), "sk-test-1234567890123456", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewService(NewRuleDetector(), secondStore).Redact(context.Background(), "sk-test-1234567890123456", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Entities[0].Replacement != second.Entities[0].Replacement {
		t.Fatalf("expected restart-stable token, got %s and %s", first.Entities[0].Replacement, second.Entities[0].Replacement)
	}
}

func TestModelEntityKeepsEntityTypeToken(t *testing.T) {
	store, err := scope.NewMemoryStore(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	detector := staticDetector{entities: []Entity{{Type: "PHONE_NUMBER", Text: "13800138000", StartByte: 0, EndByte: 11, Confidence: 0.9, Sources: []string{"model:test"}}}}
	result, err := NewService(detector, store).Redact(context.Background(), "13800138000", "")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^<PHONE_NUMBER:[A-F0-9]{4}>$`).MatchString(result.Text) {
		t.Fatalf("model token format changed: %s", result.Text)
	}
}

func TestEntityTypePolicyOnlyFiltersModelEntities(t *testing.T) {
	rules := NewRuleDetector()
	policy := rules.Policy()
	for index := range policy.EntityTypes {
		if policy.EntityTypes[index].Type == "PHONE_NUMBER" {
			policy.EntityTypes[index].Enabled = false
		}
	}
	if err := rules.Configure(policy); err != nil {
		t.Fatal(err)
	}
	input := "请联系 sk-test-1234567890123456 电话 13800138000"
	phoneStart := strings.Index(input, "13800138000")
	model := staticDetector{entities: []Entity{{Type: "PHONE_NUMBER", Text: "13800138000", StartByte: phoneStart, EndByte: phoneStart + len("13800138000"), Confidence: 0.9, Sources: []string{"model:test"}}}}
	store, err := scope.NewMemoryStore(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewPolicyDetector(NewDynamicDetector(rules, model), rules), store)
	result, err := service.Redact(context.Background(), input, "")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`<MASK_SECRET_KEY:[A-F0-9]{4}>`).MatchString(result.Text) || !strings.Contains(result.Text, "13800138000") {
		t.Fatalf("entity policy affected the wrong detector: %s", result.Text)
	}
}

func TestEntityCacheReusesDetectionAndRefreshesTTL(t *testing.T) {
	detector := &countingDetector{entities: []Entity{{Type: "EMAIL", Text: "foo@example.com", StartByte: 0, EndByte: 15}}}
	store, err := scope.NewMemoryStore(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewServiceWithCache(detector, store, EntityCacheConfig{Enabled: true, TTL: 40 * time.Millisecond, MaxEntries: 8})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if _, err := service.Detect(context.Background(), "foo@example.com"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	if _, err := service.Detect(context.Background(), "foo@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := detector.calls; got != 1 {
		t.Fatalf("detector called %d times before sliding expiry, want 1", got)
	}
	time.Sleep(25 * time.Millisecond)
	if _, err := service.Detect(context.Background(), "foo@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := detector.calls; got != 1 {
		t.Fatalf("cache hit did not refresh expiry; detector called %d times", got)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := service.Detect(context.Background(), "foo@example.com"); err != nil {
		t.Fatal(err)
	}
	if got := detector.calls; got != 2 {
		t.Fatalf("detector called %d times after expiry, want 2", got)
	}
}

func TestClearEntityCachePreventsInflightDetectionFromRepopulatingCache(t *testing.T) {
	input := "sk-test-1234567890123456"
	detector := &blockingDetector{
		started: make(chan struct{}),
		release: make(chan struct{}),
		entity:  Entity{Type: "SECRET_KEY", Text: input, StartByte: 0, EndByte: len(input)},
	}
	store, err := scope.NewMemoryStore(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewServiceWithCache(detector, store, EntityCacheConfig{Enabled: true, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	firstDone := make(chan error, 1)
	go func() {
		_, detectErr := service.Detect(context.Background(), input)
		firstDone <- detectErr
	}()
	<-detector.started
	service.ClearEntityCache()
	close(detector.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if _, err := service.Detect(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if got := detector.calls.Load(); got != 2 {
		t.Fatalf("detector called %d times, want 2 after cache invalidation", got)
	}
}

func TestRedactLogsDurationAndCacheStatus(t *testing.T) {
	detector := &countingDetector{entities: []Entity{{Type: "EMAIL", Text: "foo@example.com", StartByte: 0, EndByte: 15}}}
	store, err := scope.NewMemoryStore(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewServiceWithCache(detector, store, EntityCacheConfig{Enabled: true, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	var output bytes.Buffer
	service.SetLogger(log.New(&output, "", 0))

	if _, err := service.Redact(context.Background(), "foo@example.com", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Redact(context.Background(), "foo@example.com", ""); err != nil {
		t.Fatal(err)
	}

	logs := output.String()
	if strings.Count(logs, "redact ") != 2 {
		t.Fatalf("expected one log line per redaction, got %q", logs)
	}
	if !strings.Contains(logs, "duration_ms=") || !strings.Contains(logs, "duration_us=") {
		t.Fatalf("redaction duration missing from logs: %q", logs)
	}
	if !strings.Contains(logs, "cache_state=miss") || !strings.Contains(logs, "cache_state=hit") {
		t.Fatalf("cache status missing from logs: %q", logs)
	}
	if strings.Contains(logs, "foo@example.com") {
		t.Fatalf("redaction logs must not contain input text: %q", logs)
	}
}

func TestEntityCacheDoesNotExposeMutableEntries(t *testing.T) {
	detector := staticDetector{entities: []Entity{{Type: "EMAIL", Text: "foo@example.com", StartByte: 0, EndByte: 15, Sources: []string{"test"}}}}
	store, err := scope.NewMemoryStore(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewServiceWithCache(detector, store, EntityCacheConfig{Enabled: true, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	first, err := service.Detect(context.Background(), "foo@example.com")
	if err != nil {
		t.Fatal(err)
	}
	first[0].Type = "MUTATED"
	first[0].Sources[0] = "MUTATED"
	second, err := service.Detect(context.Background(), "foo@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Type != "EMAIL" || second[0].Sources[0] != "test" {
		t.Fatalf("cache entry was mutated through returned entities: %#v", second[0])
	}
}

func TestEntityCacheAutomaticallyRemovesExpiredEntries(t *testing.T) {
	cache, err := NewEntityCache(EntityCacheConfig{Enabled: true, TTL: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	cache.Set(context.Background(), "sensitive text", []Entity{{Type: "SECRET"}})
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) && cache.Len() != 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := cache.Len(); got != 0 {
		t.Fatalf("expired cache entry was not cleaned up, len=%d", got)
	}
}

func TestServiceFailsOpenWhenCacheBackendIsUnavailable(t *testing.T) {
	detector := &countingDetector{entities: []Entity{{Type: "EMAIL", Text: "foo@example.com"}}}
	store, err := scope.NewMemoryStore(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	service := NewServiceWithCacheBackend(detector, store, &failingEntityCache{config: DefaultEntityCacheConfig()})
	entities, err := service.Detect(context.Background(), "foo@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 1 || detector.calls != 1 {
		t.Fatalf("cache failure prevented detection: entities=%#v calls=%d", entities, detector.calls)
	}
}

func TestEntityCacheFactorySelectsRedisBackend(t *testing.T) {
	backend, err := NewEntityCacheBackend(EntityCacheConfig{
		Enabled: true, TTL: time.Minute, Backend: EntityCacheBackendRedis, RedisURL: "redis://127.0.0.1:6379/0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.(*RedisEntityCache); !ok {
		t.Fatalf("unexpected redis backend type %T", backend)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
}

type countingDetector struct {
	entities []Entity
	calls    int
}

type blockingDetector struct {
	started chan struct{}
	release chan struct{}
	entity  Entity
	calls   atomic.Int32
}

func (d *blockingDetector) ID() string { return "blocking" }
func (d *blockingDetector) Detect(context.Context, string) ([]Entity, error) {
	if d.calls.Add(1) == 1 {
		close(d.started)
		<-d.release
	}
	return []Entity{d.entity}, nil
}

type failingEntityCache struct{ config EntityCacheConfig }

func (c *failingEntityCache) Lookup(context.Context, string) ([]Entity, bool, error) {
	return nil, false, errors.New("cache unavailable")
}
func (c *failingEntityCache) Store(context.Context, string, []Entity) error {
	return errors.New("cache unavailable")
}
func (c *failingEntityCache) Configure(config EntityCacheConfig) error { c.config = config; return nil }
func (c *failingEntityCache) Config() EntityCacheConfig                { return c.config }
func (c *failingEntityCache) Clear(context.Context) error              { return nil }
func (c *failingEntityCache) Close() error                             { return nil }

func (d *countingDetector) ID() string { return "counting" }
func (d *countingDetector) Detect(context.Context, string) ([]Entity, error) {
	d.calls++
	return cloneEntities(d.entities), nil
}

type staticDetector struct{ entities []Entity }

func (d staticDetector) ID() string { return "model:test" }
func (d staticDetector) Detect(context.Context, string) ([]Entity, error) {
	return append([]Entity(nil), d.entities...), nil
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	store, err := scope.NewMemoryStore(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return NewService(NewRuleDetector(), store)
}

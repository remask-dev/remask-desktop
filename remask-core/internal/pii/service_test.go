package pii

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/remask/remask-core/internal/scope"
)

func TestRedactReturnsTextAndEntitiesThenRestores(t *testing.T) {
	service := newTestService(t)
	input := "请联系 foo@example.com"

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
		if !regexp.MustCompile(`^<MASK_[A-Z][A-Z0-9_]*:[A-F0-9]{4}>$`).MatchString(entity.Replacement) {
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
	first, err := service.Redact(context.Background(), "foo@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Redact(context.Background(), "再次联系 foo@example.com", first.ScopeID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Entities[0].Replacement != second.Entities[0].Replacement {
		t.Fatalf("expected stable token, got %s and %s", first.Entities[0].Replacement, second.Entities[0].Replacement)
	}
}

func TestSameValueReusesTokenAcrossRequestScopes(t *testing.T) {
	service := newTestService(t)
	first, err := service.Redact(context.Background(), "foo@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Redact(context.Background(), "foo@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.ScopeID == second.ScopeID {
		t.Fatal("expected independent request scopes")
	}
	if first.Entities[0].Replacement != second.Entities[0].Replacement {
		t.Fatalf("expected device-stable token, got %s and %s", first.Entities[0].Replacement, second.Entities[0].Replacement)
	}
}

func TestSameDeviceKeyReusesTokenAcrossStoreRestart(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	firstStore, err := scope.NewMemoryStore(time.Minute, key)
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := scope.NewMemoryStore(time.Minute, key)
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewService(NewRuleDetector(), firstStore).Redact(context.Background(), "foo@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewService(NewRuleDetector(), secondStore).Redact(context.Background(), "foo@example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if first.Entities[0].Replacement != second.Entities[0].Replacement {
		t.Fatalf("expected restart-stable token, got %s and %s", first.Entities[0].Replacement, second.Entities[0].Replacement)
	}
}

func TestModelEntityKeepsEntityTypeToken(t *testing.T) {
	store, err := scope.NewMemoryStore(time.Minute, []byte("0123456789abcdef0123456789abcdef"))
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
	input := "请联系 foo@example.com 电话 13800138000"
	phoneStart := strings.Index(input, "13800138000")
	model := staticDetector{entities: []Entity{{Type: "PHONE_NUMBER", Text: "13800138000", StartByte: phoneStart, EndByte: phoneStart + len("13800138000"), Confidence: 0.9, Sources: []string{"model:test"}}}}
	store, err := scope.NewMemoryStore(time.Minute, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewPolicyDetector(NewDynamicDetector(rules, model), rules), store)
	result, err := service.Redact(context.Background(), input, "")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`<MASK_EMAIL:[A-F0-9]{4}>`).MatchString(result.Text) || !strings.Contains(result.Text, "13800138000") {
		t.Fatalf("entity policy affected the wrong detector: %s", result.Text)
	}
}

type staticDetector struct{ entities []Entity }

func (d staticDetector) ID() string { return "model:test" }
func (d staticDetector) Detect(context.Context, string) ([]Entity, error) {
	return append([]Entity(nil), d.entities...), nil
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	store, err := scope.NewMemoryStore(time.Minute, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return NewService(NewRuleDetector(), store)
}

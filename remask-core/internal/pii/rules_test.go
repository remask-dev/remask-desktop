package pii

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPolicyIncludesSecretKeyRule(t *testing.T) {
	policy := DefaultPolicySettings()
	if policy.RedactSystemMessages {
		t.Fatal("system message redaction must be disabled by default")
	}
	rules := policy.Rules
	if len(rules) != 1 {
		t.Fatalf("expected one preset rule, got %#v", rules)
	}
	if rules[0].ID != "SECRET_KEY" || !rules[0].Enabled {
		t.Fatalf("unexpected preset rule: %#v", rules[0])
	}
}

func TestRuleDetectorInitializesPolicyFile(t *testing.T) {
	directory := t.TempDir()
	if _, err := NewRuleDetectorWithDataDir(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "policy.json")); err != nil {
		t.Fatalf("policy file was not initialized: %v", err)
	}
}

func TestRuleDetectorAppliesConfiguredRuleLimitToAllRules(t *testing.T) {
	detector := NewRuleDetector()
	detector.SetRuleLimitProvider(func() int { return 3 })
	policy := detector.Policy()
	policy.Rules = []RuleConfig{
		{ID: "ONE", Pattern: "one", Enabled: true},
		{ID: "TWO", Pattern: "two", Enabled: true},
		{ID: "THREE", Pattern: "three", Enabled: true},
		{ID: "FOUR", Pattern: "four", Enabled: true},
	}
	if err := detector.Configure(policy); err != nil {
		t.Fatal(err)
	}
	entities, err := detector.Detect(context.Background(), "one two three four")
	if err != nil {
		t.Fatal(err)
	}
	if len(entities) != 3 {
		t.Fatalf("limited entities = %#v", entities)
	}
	if got := detector.EffectivePolicy(); len(got.Rules) != 3 {
		t.Fatalf("effective policy rules = %#v", got.Rules)
	}
	if got := detector.Policy(); len(got.Rules) != 4 {
		t.Fatalf("stored policy rules = %#v", got.Rules)
	}
	for _, entity := range entities {
		if entity.Type == "FOUR" {
			t.Fatalf("fourth rule was active: %#v", entities)
		}
	}
}

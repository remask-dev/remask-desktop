package pii

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultPolicyOnlyIncludesEmailRule(t *testing.T) {
	rules := DefaultPolicySettings().Rules
	if len(rules) != 1 {
		t.Fatalf("expected one preset rule, got %#v", rules)
	}
	if rules[0].ID != "EMAIL" || !rules[0].Enabled {
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

package pii

import "testing"

func TestDefaultPolicyOnlyIncludesEmailRule(t *testing.T) {
	rules := DefaultPolicySettings().Rules
	if len(rules) != 1 {
		t.Fatalf("expected one preset rule, got %#v", rules)
	}
	if rules[0].ID != "EMAIL" || !rules[0].Enabled {
		t.Fatalf("unexpected preset rule: %#v", rules[0])
	}
}

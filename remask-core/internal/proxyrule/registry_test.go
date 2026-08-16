package proxyrule

import (
	"path/filepath"
	"testing"
)

func TestRegistryPersistsRules(t *testing.T) {
	directory := t.TempDir()
	registry, err := NewRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(registry.List()) != len(DefaultRules()) {
		t.Fatalf("expected default proxy rules")
	}
	item := Rule{ID: "internal", Hosts: []string{"AI.EXAMPLE.COM"}, Port: 8443, ProfileID: "openai", Enabled: true}
	if err := registry.Put(item); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	matched, ok := reloaded.MatchAuthority("ai.example.com:8443")
	if !ok || matched.ID != item.ID || matched.Hosts[0] != "ai.example.com" {
		t.Fatalf("unexpected match: %#v %t", matched, ok)
	}
	if filepath.Base(reloaded.filePath) != "proxy_rules.json" {
		t.Fatalf("unexpected persistence path: %s", reloaded.filePath)
	}
}

func TestRegistryRejectsDuplicateEnabledAuthority(t *testing.T) {
	registry, err := NewRegistry("")
	if err != nil {
		t.Fatal(err)
	}
	first := Rule{ID: "one", Hosts: []string{"api.example.com"}, Port: 443, ProfileID: "openai", Enabled: true}
	second := Rule{ID: "two", Hosts: []string{"api.example.com"}, Port: 443, ProfileID: "anthropic", Enabled: true}
	if err := registry.Put(first); err != nil {
		t.Fatal(err)
	}
	if err := registry.Put(second); err == nil {
		t.Fatal("expected duplicate authority rejection")
	}
	if _, err := registry.Get(second.ID); err == nil {
		t.Fatal("rejected rule must not remain in the registry")
	}
}

func TestDisabledRuleDoesNotMatch(t *testing.T) {
	registry, err := NewRegistry("")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Put(Rule{ID: "disabled", Hosts: []string{"api.example.com"}, Port: 443, ProfileID: "openai", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.MatchAuthority("api.example.com:443"); ok {
		t.Fatal("disabled rule unexpectedly matched")
	}
}

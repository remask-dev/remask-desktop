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

func TestRegistryMatchesHostWildcards(t *testing.T) {
	registry, err := NewRegistry("")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []Rule{
		{ID: "all", Hosts: []string{"*"}, Port: 443, ProfileID: "generic", Enabled: true},
		{ID: "example", Hosts: []string{"*.example.com"}, Port: 443, ProfileID: "openai", Enabled: true},
		{ID: "nested", Hosts: []string{"*.api.example.com"}, Port: 443, ProfileID: "openai", Enabled: true},
		{ID: "exact", Hosts: []string{"special.api.example.com"}, Port: 443, ProfileID: "anthropic", Enabled: true},
	} {
		if err := registry.Put(item); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		authority string
		wantID    string
	}{
		{authority: "unrelated.test:443", wantID: "all"},
		{authority: "example.com:443", wantID: "all"},
		{authority: "chat.example.com:443", wantID: "example"},
		{authority: "v1.api.example.com:443", wantID: "nested"},
		{authority: "special.api.example.com:443", wantID: "exact"},
	}
	for _, test := range tests {
		matched, ok := registry.MatchAuthority(test.authority)
		if !ok || matched.ID != test.wantID {
			t.Errorf("MatchAuthority(%q) = %#v, %t; want rule %q", test.authority, matched, ok, test.wantID)
		}
	}
}

func TestRegistryWildcardStillHonorsPort(t *testing.T) {
	registry, err := NewRegistry("")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Put(Rule{ID: "https", Hosts: []string{"*"}, Port: 443, ProfileID: "openai", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.MatchAuthority("api.example.com:8443"); ok {
		t.Fatal("wildcard unexpectedly matched a different port")
	}
}

func TestRegistryRejectsUnsupportedHostWildcards(t *testing.T) {
	registry, err := NewRegistry("")
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"api.*.example.com", "example.*", "**.example.com"} {
		item := Rule{ID: host, Hosts: []string{host}, Port: 443, ProfileID: "openai", Enabled: true}
		if err := registry.Put(item); err == nil {
			t.Errorf("expected wildcard %q to be rejected", host)
		}
	}
}

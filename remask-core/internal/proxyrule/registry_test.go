package proxyrule

import (
	"os"
	"path/filepath"
	"strings"
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
	if !ok || matched.ID != item.ID || matched.Hosts[0] != "ai.example.com:8443" || matched.Port != 0 {
		t.Fatalf("unexpected match: %#v %t", matched, ok)
	}
	if filepath.Base(reloaded.filePath) != "proxy_rules.json" {
		t.Fatalf("unexpected persistence path: %s", reloaded.filePath)
	}
}

func TestRegistryMigratesLegacySharedPort(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "proxy_rules.json")
	legacy := `[{"id":"legacy","hosts":["API.EXAMPLE.COM","other.example.com"],"port":8443,"profile_id":"openai","enabled":true}]`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	registry, err := NewRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, authority := range []string{"api.example.com:8443", "other.example.com:8443"} {
		if _, ok := registry.MatchAuthority(authority); !ok {
			t.Errorf("legacy target %q did not match", authority)
		}
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), `"port"`) || !strings.Contains(string(persisted), `"api.example.com:8443"`) {
		t.Fatalf("legacy rule was not migrated: %s", persisted)
	}
}

func TestRegistrySupportsPerTargetPortsAndAnyPort(t *testing.T) {
	registry, err := NewRegistry("")
	if err != nil {
		t.Fatal(err)
	}
	item := Rule{
		ID:        "mixed",
		Hosts:     []string{"AAA.EXAMPLE.COM:8443", "b.example.com", "*:80"},
		ProfileID: "openai",
		Enabled:   true,
	}
	if err := registry.Put(item); err != nil {
		t.Fatal(err)
	}

	for _, authority := range []string{"aaa.example.com:8443", "b.example.com:443", "b.example.com:9443", "other.example.com:80"} {
		matched, ok := registry.MatchAuthority(authority)
		if !ok || matched.ID != item.ID {
			t.Errorf("MatchAuthority(%q) = %#v, %t; want mixed", authority, matched, ok)
		}
	}
	if _, ok := registry.MatchAuthority("aaa.example.com:443"); ok {
		t.Fatal("port-specific target unexpectedly matched a different port")
	}
}

func TestRegistryPrefersPortSpecificTargetOverAnyPort(t *testing.T) {
	registry, err := NewRegistry("")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []Rule{
		{ID: "any-port", Hosts: []string{"api.example.com"}, ProfileID: "openai", Enabled: true},
		{ID: "https", Hosts: []string{"api.example.com:443"}, ProfileID: "anthropic", Enabled: true},
	} {
		if err := registry.Put(item); err != nil {
			t.Fatal(err)
		}
	}

	matched, ok := registry.MatchAuthority("api.example.com:443")
	if !ok || matched.ID != "https" {
		t.Fatalf("unexpected HTTPS match: %#v %t", matched, ok)
	}
	matched, ok = registry.MatchAuthority("api.example.com:8443")
	if !ok || matched.ID != "any-port" {
		t.Fatalf("unexpected fallback match: %#v %t", matched, ok)
	}
}

func TestRegistryNormalizesIPv6Targets(t *testing.T) {
	registry, err := NewRegistry("")
	if err != nil {
		t.Fatal(err)
	}
	item := Rule{ID: "ipv6", Hosts: []string{"[2001:DB8::1]:8443", "2001:DB8::2"}, ProfileID: "openai", Enabled: true}
	if err := registry.Put(item); err != nil {
		t.Fatal(err)
	}
	stored, err := registry.Get(item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Hosts[0] != "2001:db8::2" || stored.Hosts[1] != "[2001:db8::1]:8443" {
		t.Fatalf("unexpected normalized targets: %#v", stored.Hosts)
	}
	for _, authority := range []string{"[2001:db8::1]:8443", "[2001:db8::2]:443"} {
		if _, ok := registry.MatchAuthority(authority); !ok {
			t.Errorf("expected IPv6 target %q to match", authority)
		}
	}
}

func TestRegistryRejectsInvalidTargetPorts(t *testing.T) {
	registry, err := NewRegistry("")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"api.example.com:0", "api.example.com:65536", "api.example.com:https", ":443", "2001:db8::not-an-ip:443"} {
		item := Rule{ID: target, Hosts: []string{target}, ProfileID: "openai", Enabled: true}
		if err := registry.Put(item); err == nil {
			t.Errorf("expected target %q to be rejected", target)
		}
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

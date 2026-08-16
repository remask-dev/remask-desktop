package upstream

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistrySeedsBuiltinAIUpstreamsOnFirstInitialization(t *testing.T) {
	directory := t.TempDir()
	registry, err := NewRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	items := registry.List()
	expected := DefaultUpstreams()
	if len(items) != len(expected) {
		t.Fatalf("unexpected preset upstreams: %#v", items)
	}
	for index := range expected {
		if items[index] != expected[index] {
			t.Fatalf("preset upstream %d = %#v, expected %#v", index, items[index], expected[index])
		}
	}
	if _, err := os.Stat(filepath.Join(directory, "upstreams.json")); err != nil {
		t.Fatalf("preset upstream was not persisted: %v", err)
	}
}

func TestRegistryDoesNotReplaceExistingConfiguration(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "upstreams.json"), []byte("[]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	if items := registry.List(); len(items) != 0 {
		t.Fatalf("existing configuration was replaced: %#v", items)
	}
}

func TestRegistryMigratesLegacyOpenAIDefault(t *testing.T) {
	directory := t.TempDir()
	legacy := `[{"id":"openai","base_url":"https://api.openai.com","profile_id":"openai","credential_mode":"passthrough"}]`
	if err := os.WriteFile(filepath.Join(directory, "upstreams.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	if items := registry.List(); len(items) != len(DefaultUpstreams()) {
		t.Fatalf("legacy defaults were not migrated: %#v", items)
	}
}

func TestRegistryDefaultsLegacyEnabledStateAndPersistsDisabledState(t *testing.T) {
	directory := t.TempDir()
	legacy := `[{"id":"custom","base_url":"https://example.com","profile_id":"openai","credential_mode":"passthrough"}]`
	if err := os.WriteFile(filepath.Join(directory, "upstreams.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	item, err := registry.Get("custom")
	if err != nil || !item.Enabled {
		t.Fatalf("legacy upstream should default to enabled: %#v, %v", item, err)
	}
	item.Enabled = false
	if err := registry.Put(item); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	item, err = reloaded.Get("custom")
	if err != nil || item.Enabled {
		t.Fatalf("disabled upstream was not persisted: %#v, %v", item, err)
	}
}

func TestRegistryPersistsUpstreamsAndMasksHeaders(t *testing.T) {
	directory := t.TempDir()
	registry, err := NewRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	item := Upstream{ID: "managed", BaseURL: "https://example.com", ProfileID: "openai", CredentialMode: "managed", APIKey: "secret"}
	if err := registry.Put(item); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := loaded.Get("managed")
	if err != nil || stored.APIKey != "secret" {
		t.Fatalf("unexpected stored upstream: %#v, %v", stored, err)
	}
	if public := stored.Public(); public.APIKey != "••••••••" {
		t.Fatalf("public upstream exposed secret: %#v", public)
	}
}

func TestRegistryRemovesManagedHeadersWhenSwitchingToPassthrough(t *testing.T) {
	registry, err := NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	item := Upstream{ID: "service", BaseURL: "https://example.com", ProfileID: "openai", CredentialMode: "managed", APIKey: "secret"}
	if err := registry.Put(item); err != nil {
		t.Fatal(err)
	}
	item.CredentialMode = "passthrough"
	if err := registry.Put(item); err != nil {
		t.Fatal(err)
	}
	stored, err := registry.Get("service")
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIKey != "" {
		t.Fatalf("passthrough upstream retained managed API key: %#v", stored.APIKey)
	}
}

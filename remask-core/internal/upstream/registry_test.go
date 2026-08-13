package upstream

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRegistrySeedsOpenAIOnFirstInitialization(t *testing.T) {
	directory := t.TempDir()
	registry, err := NewRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	items := registry.List()
	if len(items) != 1 || items[0] != DefaultUpstreams()[0] {
		t.Fatalf("unexpected preset upstreams: %#v", items)
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

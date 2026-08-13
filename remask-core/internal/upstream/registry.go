package upstream

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var ErrNotFound = errors.New("upstream not found")

type Upstream struct {
	ID             string `json:"id"`
	BaseURL        string `json:"base_url"`
	ProfileID      string `json:"profile_id"`
	DefaultPolicy  string `json:"default_policy,omitempty"`
	CredentialMode string `json:"credential_mode"`
	APIKey         string `json:"api_key,omitempty"`
}

func (u Upstream) Validate() error {
	if strings.TrimSpace(u.ID) == "" || strings.ContainsAny(u.ID, "/\\") {
		return errors.New("invalid upstream id")
	}
	parsed, err := url.Parse(u.BaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return errors.New("base_url must be an absolute HTTP URL without user info")
	}
	if u.ProfileID == "" {
		return errors.New("profile_id is required")
	}
	if u.CredentialMode == "" {
		u.CredentialMode = "passthrough"
	}
	if u.CredentialMode != "passthrough" && u.CredentialMode != "managed" {
		return errors.New("credential_mode must be passthrough or managed")
	}
	if u.CredentialMode == "managed" && strings.TrimSpace(u.APIKey) == "" {
		return errors.New("api_key is required for managed credentials")
	}
	return nil
}

type Registry struct {
	mu       sync.RWMutex
	items    map[string]Upstream
	filePath string
}

func DefaultUpstreams() []Upstream {
	return []Upstream{{
		ID: "openai", BaseURL: "https://api.openai.com", ProfileID: "openai", CredentialMode: "passthrough",
	}}
}

func NewRegistry(dataDir string) (*Registry, error) {
	registry := &Registry{items: make(map[string]Upstream)}
	if strings.TrimSpace(dataDir) == "" {
		return registry, nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	registry.filePath = filepath.Join(dataDir, "upstreams.json")
	data, err := os.ReadFile(registry.filePath)
	if errors.Is(err, os.ErrNotExist) {
		for _, item := range DefaultUpstreams() {
			registry.items[item.ID] = item
		}
		if err := registry.persistLocked(); err != nil {
			return nil, err
		}
		return registry, nil
	}
	if err != nil {
		return nil, err
	}
	var items []Upstream
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	for _, item := range items {
		if err := item.Validate(); err != nil {
			return nil, err
		}
		if _, exists := registry.items[item.ID]; exists {
			return nil, errors.New("duplicate upstream id")
		}
		registry.items[item.ID] = item
	}
	return registry, nil
}

func (r *Registry) Put(item Upstream) error {
	if item.CredentialMode == "" {
		item.CredentialMode = "passthrough"
	}
	if item.CredentialMode == "passthrough" {
		item.APIKey = ""
	}
	if err := item.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[item.ID] = item
	return r.persistLocked()
}

func (r *Registry) Get(id string) (Upstream, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.items[id]
	if !ok {
		return Upstream{}, ErrNotFound
	}
	return item, nil
}

func (r *Registry) List() []Upstream {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Upstream, 0, len(r.items))
	for _, item := range r.items {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[id]; !ok {
		return ErrNotFound
	}
	delete(r.items, id)
	return r.persistLocked()
}

func (r *Registry) persistLocked() error {
	if r.filePath == "" {
		return nil
	}
	items := make([]Upstream, 0, len(r.items))
	for _, item := range r.items {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	temporary := r.filePath + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, r.filePath)
}

func (u Upstream) Public() Upstream {
	copy := u
	if u.APIKey != "" {
		copy.APIKey = "••••••••"
	}
	return copy
}

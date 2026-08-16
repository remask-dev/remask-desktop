package proxyrule

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var ErrNotFound = errors.New("proxy rule not found")

// Rule describes which explicit-proxy destinations Remask may inspect. The
// destination itself always comes from the proxy request; a rule only selects
// the protocol profile used by the protection pipeline.
type Rule struct {
	ID        string   `json:"id"`
	Hosts     []string `json:"hosts"`
	Port      int      `json:"port,omitempty"`
	ProfileID string   `json:"profile_id"`
	Enabled   bool     `json:"enabled"`
}

func (r Rule) Validate() error {
	if strings.TrimSpace(r.ID) == "" || strings.ContainsAny(r.ID, "/\\") {
		return errors.New("invalid proxy rule id")
	}
	if len(r.Hosts) == 0 {
		return errors.New("at least one host is required")
	}
	seen := make(map[string]bool, len(r.Hosts))
	for _, host := range r.Hosts {
		host = normalizeHost(host)
		if host == "" || net.ParseIP(host) == nil && strings.ContainsAny(host, " /:@") {
			return errors.New("hosts must contain DNS names or IP addresses")
		}
		if seen[host] {
			return errors.New("hosts must be unique")
		}
		seen[host] = true
	}
	if r.Port < 0 || r.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if strings.TrimSpace(r.ProfileID) == "" {
		return errors.New("profile_id is required")
	}
	return nil
}

type Registry struct {
	mu       sync.RWMutex
	items    map[string]Rule
	filePath string
}

func DefaultRules() []Rule {
	return []Rule{
		{ID: "anthropic-api", Hosts: []string{"api.anthropic.com"}, Port: 443, ProfileID: "anthropic", Enabled: true},
		{ID: "chatgpt", Hosts: []string{"chatgpt.com"}, Port: 443, ProfileID: "codex-chatgpt", Enabled: true},
		{ID: "openai-api", Hosts: []string{"api.openai.com"}, Port: 443, ProfileID: "openai", Enabled: true},
	}
}

func NewRegistry(dataDir string) (*Registry, error) {
	return NewRegistryWithDefaults(dataDir, DefaultRules())
}

// NewRegistryWithDefaults seeds a new registry only when no persisted proxy
// rule file exists. Callers can use this for one-time migration without
// coupling future proxy-rule edits back to Provider configuration.
func NewRegistryWithDefaults(dataDir string, defaults []Rule) (*Registry, error) {
	registry := &Registry{items: make(map[string]Rule)}
	if strings.TrimSpace(dataDir) == "" {
		return registry, nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	registry.filePath = filepath.Join(dataDir, "proxy_rules.json")
	data, err := os.ReadFile(registry.filePath)
	if errors.Is(err, os.ErrNotExist) {
		for _, item := range defaults {
			item = normalizeRule(item)
			if err := item.Validate(); err != nil {
				return nil, err
			}
			registry.items[item.ID] = item
		}
		if err := registry.validateAuthoritiesLocked(); err != nil {
			return nil, err
		}
		if err := registry.persistLocked(); err != nil {
			return nil, err
		}
		return registry, nil
	}
	if err != nil {
		return nil, err
	}
	var items []Rule
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	for _, item := range items {
		item = normalizeRule(item)
		if err := item.Validate(); err != nil {
			return nil, err
		}
		if _, exists := registry.items[item.ID]; exists {
			return nil, errors.New("duplicate proxy rule id")
		}
		registry.items[item.ID] = item
	}
	if err := registry.validateAuthoritiesLocked(); err != nil {
		return nil, err
	}
	return registry, nil
}

func (r *Registry) Put(item Rule) error {
	item = normalizeRule(item)
	if err := item.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	previous, existed := r.items[item.ID]
	r.items[item.ID] = item
	if err := r.validateAuthoritiesLocked(); err != nil {
		if existed {
			r.items[item.ID] = previous
		} else {
			delete(r.items, item.ID)
		}
		return err
	}
	return r.persistLocked()
}

func (r *Registry) Get(id string) (Rule, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.items[id]
	if !ok {
		return Rule{}, ErrNotFound
	}
	return cloneRule(item), nil
}

func (r *Registry) List() []Rule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Rule, 0, len(r.items))
	for _, item := range r.items {
		result = append(result, cloneRule(item))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (r *Registry) MatchAuthority(authority string) (Rule, bool) {
	host, port := splitAuthority(authority)
	if host == "" {
		return Rule{}, false
	}
	for _, item := range r.List() {
		if !item.Enabled || item.Port != 0 && port != 0 && item.Port != port {
			continue
		}
		for _, candidate := range item.Hosts {
			if normalizeHost(candidate) == host {
				return item, true
			}
		}
	}
	return Rule{}, false
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

func (r *Registry) validateAuthoritiesLocked() error {
	owners := make(map[string]string)
	for _, item := range r.items {
		if !item.Enabled {
			continue
		}
		for _, host := range item.Hosts {
			key := normalizeHost(host) + ":" + strconv.Itoa(item.Port)
			if owner, exists := owners[key]; exists && owner != item.ID {
				return errors.New("enabled proxy rule authorities must be unique")
			}
			owners[key] = item.ID
		}
	}
	return nil
}

func (r *Registry) persistLocked() error {
	if r.filePath == "" {
		return nil
	}
	items := make([]Rule, 0, len(r.items))
	for _, item := range r.items {
		items = append(items, cloneRule(item))
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

func normalizeRule(item Rule) Rule {
	item.ID = strings.TrimSpace(item.ID)
	item.ProfileID = strings.TrimSpace(item.ProfileID)
	for index := range item.Hosts {
		item.Hosts[index] = normalizeHost(item.Hosts[index])
	}
	sort.Strings(item.Hosts)
	return item
}

func cloneRule(item Rule) Rule {
	item.Hosts = append([]string(nil), item.Hosts...)
	return item
}

func splitAuthority(authority string) (string, int) {
	authority = strings.TrimSpace(authority)
	parsed, err := url.Parse("//" + authority)
	if err != nil || parsed.Hostname() == "" {
		return "", 0
	}
	port := 0
	if parsed.Port() != "" {
		port, _ = strconv.Atoi(parsed.Port())
	}
	return normalizeHost(parsed.Hostname()), port
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(host), "[] ."))
}

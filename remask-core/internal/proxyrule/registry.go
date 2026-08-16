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
	ID    string   `json:"id"`
	Hosts []string `json:"hosts"`
	// Port is retained only to migrate rules written by older versions. New
	// rules encode an optional port in each Hosts entry (for example,
	// "api.example.com:8443"); an entry without a port matches every port.
	Port      int    `json:"port,omitempty"`
	ProfileID string `json:"profile_id"`
	Enabled   bool   `json:"enabled"`
}

func (r Rule) Validate() error {
	if strings.TrimSpace(r.ID) == "" || strings.ContainsAny(r.ID, "/\\") {
		return errors.New("invalid proxy rule id")
	}
	if len(r.Hosts) == 0 {
		return errors.New("at least one target is required")
	}
	if r.Port < 0 || r.Port > 65535 {
		return errors.New("target port must be between 1 and 65535")
	}
	seen := make(map[string]bool, len(r.Hosts))
	for _, value := range r.Hosts {
		target := normalizeTarget(value, r.Port)
		host, _, _, err := parseTarget(target)
		if err != nil {
			return err
		}
		if host == "" || net.ParseIP(host) == nil && strings.ContainsAny(host, " /:@") {
			return errors.New("targets must contain DNS names or IP addresses")
		}
		if strings.Contains(host, "*") && host != "*" && (!strings.HasPrefix(host, "*.") || strings.Count(host, "*") != 1 || len(host) <= 2) {
			return errors.New("host wildcards must be * or start with *.")
		}
		if seen[target] {
			return errors.New("targets must be unique")
		}
		seen[target] = true
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
		{ID: "anthropic-api", Hosts: []string{"api.anthropic.com:443"}, ProfileID: "anthropic", Enabled: true},
		{ID: "chatgpt", Hosts: []string{"chatgpt.com:443"}, ProfileID: "codex-chatgpt", Enabled: true},
		{ID: "openai-api", Hosts: []string{"api.openai.com:443"}, ProfileID: "openai", Enabled: true},
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
	migrated := false
	for _, item := range items {
		migrated = migrated || item.Port != 0
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
	if migrated {
		if err := registry.persistLocked(); err != nil {
			return nil, err
		}
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
	var best Rule
	bestHostKind, bestHostLength, bestPortKind := 0, 0, 0
	for _, item := range r.List() {
		if !item.Enabled {
			continue
		}
		for _, target := range item.Hosts {
			candidate, candidatePort, _, _ := parseTarget(target)
			if candidatePort != 0 && candidatePort != port {
				continue
			}
			hostKind, hostLength, matched := matchHost(candidate, host)
			if !matched {
				continue
			}
			portKind := 0
			if candidatePort != 0 {
				portKind = 1
			}
			if hostKind > bestHostKind ||
				hostKind == bestHostKind && hostLength > bestHostLength ||
				hostKind == bestHostKind && hostLength == bestHostLength && portKind > bestPortKind {
				best = item
				bestHostKind, bestHostLength, bestPortKind = hostKind, hostLength, portKind
			}
		}
	}
	return best, bestHostKind != 0
}

// matchHost returns a match category and pattern length so callers can prefer
// exact hosts over subdomain wildcards, and narrower wildcards over broader
// ones. A subdomain wildcard does not match the apex domain itself.
func matchHost(pattern, host string) (kind int, length int, matched bool) {
	pattern = normalizeHost(pattern)
	host = normalizeHost(host)
	switch {
	case pattern == host:
		return 3, len(pattern), true
	case pattern == "*":
		return 1, 0, true
	case strings.HasPrefix(pattern, "*."):
		suffix := pattern[1:]
		if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
			return 2, len(suffix), true
		}
	}
	return 0, 0, false
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
		for _, target := range item.Hosts {
			key := target
			if owner, exists := owners[key]; exists && owner != item.ID {
				return errors.New("enabled proxy rule targets must be unique")
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
		item.Hosts[index] = normalizeTarget(item.Hosts[index], item.Port)
	}
	item.Port = 0
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

func parseTarget(target string) (host string, port int, hasPort bool, err error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", 0, false, errors.New("targets must not be empty")
	}

	if strings.HasPrefix(target, "[") {
		closing := strings.IndexByte(target, ']')
		if closing < 0 {
			return "", 0, false, errors.New("invalid bracketed target")
		}
		host = normalizeHost(target[1:closing])
		remainder := target[closing+1:]
		if remainder == "" {
			if net.ParseIP(host) == nil {
				return "", 0, false, errors.New("brackets are only valid for IPv6 targets")
			}
			return host, 0, false, nil
		}
		if !strings.HasPrefix(remainder, ":") || len(remainder) == 1 {
			return "", 0, false, errors.New("invalid target port")
		}
		port, err = parseTargetPort(remainder[1:])
		return host, port, true, err
	}

	if net.ParseIP(target) != nil {
		return normalizeHost(target), 0, false, nil
	}
	switch strings.Count(target, ":") {
	case 0:
		return normalizeHost(target), 0, false, nil
	case 1:
		separator := strings.LastIndexByte(target, ':')
		host = normalizeHost(target[:separator])
		port, err = parseTargetPort(target[separator+1:])
		return host, port, true, err
	default:
		return "", 0, false, errors.New("IPv6 targets with a port must use [address]:port")
	}
}

func parseTargetPort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("target port must be between 1 and 65535")
	}
	return port, nil
}

func normalizeTarget(target string, legacyPort int) string {
	host, port, hasPort, err := parseTarget(target)
	if err != nil {
		return strings.TrimSpace(target)
	}
	if !hasPort && legacyPort != 0 {
		port = legacyPort
	}
	if port == 0 {
		return host
	}
	if strings.Contains(host, ":") {
		return net.JoinHostPort(host, strconv.Itoa(port))
	}
	return host + ":" + strconv.Itoa(port)
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(host), "[] ."))
}

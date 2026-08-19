package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var ErrNoMatch = errors.New("profile operation not matched")

type Operation struct {
	ID                   string   `json:"id"`
	Methods              []string `json:"methods"`
	Paths                []string `json:"paths"`
	Passthrough          bool     `json:"passthrough,omitempty"`
	RequestTextFields    []string `json:"request_text_fields,omitempty"`
	SystemTextFields     []string `json:"system_text_fields,omitempty"`
	AssistantRoleFields  []string `json:"assistant_role_fields,omitempty"`
	AssistantRoles       []string `json:"assistant_roles,omitempty"`
	ResponseTextFields   []string `json:"response_text_fields,omitempty"`
	StreamTextFields     []string `json:"stream_text_fields,omitempty"`
	StreamChannelFields  []string `json:"stream_channel_fields,omitempty"`
	StreamTerminalData   []string `json:"stream_terminal_data,omitempty"`
	StreamTerminalEvents []string `json:"stream_terminal_events,omitempty"`
}

type Profile struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Operations      []Operation       `json:"operations"`
	HeaderTemplates map[string]string `json:"header_templates,omitempty"`
}

type Registry struct {
	mu       sync.RWMutex
	profiles map[string]Profile
	builtins map[string]Profile
	dir      string
}

const ExampleFileName = "example-openai-compatible.json"

// EnsureExampleFile creates one complete, usable adapter profile when the
// profiles directory is first initialized. An existing directory is never
// modified, even if the user has removed every profile from it.
func EnsureExampleFile(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	if _, err := os.Stat(dir); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var example Profile
	for _, item := range Builtins() {
		if item.ID == "openai" {
			example = item
			break
		}
	}
	example.ID = "example-openai-compatible"
	example.Name = "Example · OpenAI-compatible API"
	data, err := json.MarshalIndent(example, "", "  ")
	if err != nil {
		return err
	}
	filePath := filepath.Join(dir, ExampleFileName)
	temporary := filePath + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, filePath)
}

func NewRegistry(profiles ...Profile) *Registry {
	registry := &Registry{profiles: make(map[string]Profile, len(profiles)), builtins: make(map[string]Profile, len(profiles))}
	for _, item := range profiles {
		registry.profiles[item.ID] = item
		registry.builtins[item.ID] = item
	}
	return registry
}

// LoadDir loads user-defined request adapter profiles from JSON files in dir.
// A file may contain one profile, an array of profiles, or {"profiles": [...]}.
// User profiles replace built-ins with the same ID so an adapter can be
// updated without changing provider/target configuration.
func (r *Registry) LoadDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	next := make(map[string]Profile, len(r.builtins))
	for id, item := range r.builtins {
		next[id] = item
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("read profile %q: %w", entry.Name(), err)
		}
		items, err := decodeProfiles(data)
		if err != nil {
			return fmt.Errorf("decode profile %q: %w", entry.Name(), err)
		}
		for _, item := range items {
			if strings.TrimSpace(item.ID) == "" {
				return fmt.Errorf("profile %q has an empty id", entry.Name())
			}
			next[item.ID] = item
		}
	}
	r.mu.Lock()
	r.profiles = next
	r.dir = dir
	r.mu.Unlock()
	return nil
}

// Refresh rescans the previously configured directory. It is safe to call
// while the gateway is serving requests; the registry is replaced atomically.
func (r *Registry) Refresh() error {
	r.mu.RLock()
	dir := r.dir
	r.mu.RUnlock()
	return r.LoadDir(dir)
}

func decodeProfiles(data []byte) ([]Profile, error) {
	var list []Profile
	if err := json.Unmarshal(data, &list); err == nil {
		return list, nil
	}
	var wrapper struct {
		Profiles []Profile `json:"profiles"`
	}
	if err := json.Unmarshal(data, &wrapper); err == nil && wrapper.Profiles != nil {
		return wrapper.Profiles, nil
	}
	var item Profile
	if err := json.Unmarshal(data, &item); err != nil {
		return nil, err
	}
	return []Profile{item}, nil
}

func (r *Registry) List() []Profile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Profile, 0, len(r.profiles))
	for _, item := range r.profiles {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (r *Registry) Get(id string) (Profile, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.profiles[id]
	return item, ok
}

func (r *Registry) Match(profileID, method, requestPath string) (Operation, error) {
	item, ok := r.Get(profileID)
	if !ok {
		return Operation{}, ErrNoMatch
	}
	for _, operation := range item.Operations {
		if methodMatches(operation.Methods, method) && pathMatches(operation.Paths, requestPath) {
			return operation, nil
		}
	}
	return Operation{}, ErrNoMatch
}

// GenericMatch returns the provider-agnostic operation for a POST body that
// looks like a model request. The body check deliberately happens only after
// normal method/path matching has failed, so ordinary unknown POST endpoints
// continue to pass through unchanged.
func GenericMatch(method string, body []byte) (Operation, error) {
	if !strings.EqualFold(method, "POST") || len(body) == 0 {
		return Operation{}, ErrNoMatch
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil || request == nil {
		return Operation{}, ErrNoMatch
	}
	for _, key := range []string{"model", "messages", "input", "contents"} {
		if value, ok := request[key]; ok && len(value) > 0 && string(value) != "null" {
			return GenericOperation(), nil
		}
	}
	return Operation{}, ErrNoMatch
}

// GenericOperation combines the text and stream shapes used by OpenAI,
// Anthropic, and Gemini. Selectors are intentionally broad enough for custom
// provider paths while remaining limited to known request/response fields.
func GenericOperation() Operation {
	return Operation{
		ID:      "generic-model-request",
		Methods: []string{"POST"},
		Paths:   []string{"*"},
		RequestTextFields: []string{
			"/prompt",
			"/instructions", "/input", "/input/*/content", "/input/*/content/*/text", "/input/*/content/*/content", "/input/*/arguments", "/input/*/output",
			"/system", "/system/*/text",
			"/messages/*/content", "/messages/*/content/*/text", "/messages/*/content/*/content", "/messages/*/content/*/content/*/text",
			"/system_instruction/parts/*/text", "/systemInstruction/parts/*/text",
			"/contents/*/parts/*/text",
		},
		SystemTextFields: []string{
			"/instructions", "/system", "/system/*/text",
			"/system_instruction/parts/*/text", "/systemInstruction/parts/*/text",
		},
		AssistantRoleFields: []string{"/messages/*/role", "/input/*/role", "/contents/*/role"},
		AssistantRoles:      []string{"assistant", "model"},
		ResponseTextFields: []string{
			"/choices/*/text", "/choices/*/message/content", "/content/*/text", "/candidates/*/content/parts/*/text",
			"/output/*/content/*/text", "/output/*/arguments",
		},
		StreamTextFields: []string{
			"/choices/*/text", "/choices/*/delta/content", "/delta", "/delta/text", "/delta/partial_json",
			"/candidates/*/content/parts/*/text",
		},
		StreamChannelFields:  []string{"/choices/*/index", "/output_index", "/content_index", "/item_id", "/index"},
		StreamTerminalData:   []string{"[DONE]"},
		StreamTerminalEvents: []string{"message_stop"},
	}
}

func methodMatches(methods []string, method string) bool {
	for _, candidate := range methods {
		if candidate == "*" || strings.EqualFold(candidate, method) {
			return true
		}
	}
	return false
}

func pathMatches(patterns []string, requestPath string) bool {
	for _, pattern := range patterns {
		matched, err := path.Match(pattern, requestPath)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func Builtins() []Profile {
	providers := []Profile{
		{
			ID: "openai", Name: "OpenAI", HeaderTemplates: map[string]string{"Authorization": "Bearer {{api_key}}"},
			Operations: []Operation{
				{ID: "list-models", Methods: []string{"GET"}, Paths: []string{"/v1/models"}, Passthrough: true},
				{ID: "create-chat-completion", Methods: []string{"POST"}, Paths: []string{"/v1/chat/completions"}, RequestTextFields: []string{"/messages/*/content", "/messages/*/content/*/text"}, AssistantRoleFields: []string{"/messages/*/role"}, AssistantRoles: []string{"assistant"}, ResponseTextFields: []string{"/choices/*/message/content"}, StreamTextFields: []string{"/choices/*/delta/content"}, StreamChannelFields: []string{"/choices/*/index"}, StreamTerminalData: []string{"[DONE]"}},
				{ID: "create-response", Methods: []string{"POST"}, Paths: []string{"/v1/responses"}, RequestTextFields: []string{"/instructions", "/input", "/input/*/content", "/input/*/content/*/text", "/input/*/content/*/content", "/input/*/arguments", "/input/*/output"}, SystemTextFields: []string{"/instructions"}, AssistantRoleFields: []string{"/input/*/role"}, AssistantRoles: []string{"assistant"}, ResponseTextFields: []string{"/output/*/content/*/text", "/output/*/arguments"}, StreamTextFields: []string{"/delta"}, StreamChannelFields: []string{"/output_index", "/content_index", "/item_id"}},
			},
		},
		{
			ID: "anthropic", Name: "Anthropic Messages", HeaderTemplates: map[string]string{"x-api-key": "{{api_key}}", "anthropic-version": "2023-06-01"},
			Operations: []Operation{
				{ID: "list-models", Methods: []string{"GET"}, Paths: []string{"/v1/models"}, Passthrough: true},
				{
					ID: "create-message", Methods: []string{"POST"}, Paths: []string{"/v1/messages"},
					RequestTextFields:    []string{"/system", "/system/*/text", "/messages/*/content", "/messages/*/content/*/text", "/messages/*/content/*/content", "/messages/*/content/*/content/*/text"},
					SystemTextFields:     []string{"/system", "/system/*/text"},
					AssistantRoleFields:  []string{"/messages/*/role"},
					AssistantRoles:       []string{"assistant"},
					ResponseTextFields:   []string{"/content/*/text"},
					StreamTextFields:     []string{"/delta/text", "/delta/partial_json"},
					StreamChannelFields:  []string{"/index"},
					StreamTerminalEvents: []string{"message_stop"},
				},
			},
		},
		{
			ID: "codex-chatgpt", Name: "Codex (ChatGPT login)",
			Operations: []Operation{
				{
					ID: "create-codex-response", Methods: []string{"POST"},
					Paths:               []string{"/backend-api/codex/responses", "/backend-api/codex/responses/*", "/backend-api/api/codex", "/backend-api/api/codex/responses", "/backend-api/api/codex/responses/*"},
					RequestTextFields:   []string{"/instructions", "/input", "/input/*/content", "/input/*/content/*/text", "/input/*/content/*/content", "/input/*/arguments", "/input/*/output"},
					SystemTextFields:    []string{"/instructions"},
					AssistantRoleFields: []string{"/input/*/role"},
					AssistantRoles:      []string{"assistant"},
					ResponseTextFields:  []string{"/output/*/content/*/text", "/output/*/arguments"},
					StreamTextFields:    []string{"/delta"},
					StreamChannelFields: []string{"/output_index", "/content_index", "/item_id"},
				},
			},
		},
		{
			ID: "gemini", Name: "Gemini Generate Content", HeaderTemplates: map[string]string{"x-goog-api-key": "{{api_key}}"},
			Operations: []Operation{
				{ID: "list-models", Methods: []string{"GET"}, Paths: []string{"/v1beta/models"}, Passthrough: true},
				{
					ID: "generate-content", Methods: []string{"POST"}, Paths: []string{"/v1beta/models/*:generateContent", "/v1beta/models/*:streamGenerateContent"},
					RequestTextFields:   []string{"/system_instruction/parts/*/text", "/contents/*/parts/*/text"},
					SystemTextFields:    []string{"/system_instruction/parts/*/text"},
					AssistantRoleFields: []string{"/contents/*/role"},
					AssistantRoles:      []string{"model"},
					ResponseTextFields:  []string{"/candidates/*/content/parts/*/text"},
					StreamTextFields:    []string{"/candidates/*/content/parts/*/text"},
				},
			},
		},
	}
	generic := Profile{ID: "generic", Name: "Generic AI APIs"}
	for _, provider := range providers {
		generic.Operations = append(generic.Operations, provider.Operations...)
	}
	return append([]Profile{generic}, providers...)
}

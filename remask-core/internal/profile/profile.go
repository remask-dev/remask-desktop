package profile

import (
	"errors"
	"path"
	"strings"
	"sync"
)

var ErrNoMatch = errors.New("profile operation not matched")

type Operation struct {
	ID                   string   `json:"id"`
	Methods              []string `json:"methods"`
	Paths                []string `json:"paths"`
	Passthrough          bool     `json:"passthrough,omitempty"`
	RequestTextFields    []string `json:"request_text_fields"`
	AssistantRoleFields  []string `json:"assistant_role_fields,omitempty"`
	AssistantRoles       []string `json:"assistant_roles,omitempty"`
	ResponseTextFields   []string `json:"response_text_fields"`
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
}

func NewRegistry(profiles ...Profile) *Registry {
	registry := &Registry{profiles: make(map[string]Profile, len(profiles))}
	for _, item := range profiles {
		registry.profiles[item.ID] = item
	}
	return registry
}

func (r *Registry) List() []Profile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Profile, 0, len(r.profiles))
	for _, item := range r.profiles {
		result = append(result, item)
	}
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
	return []Profile{
		{
			ID: "openai", Name: "OpenAI", HeaderTemplates: map[string]string{"Authorization": "Bearer {{api_key}}"},
			Operations: []Operation{
				{ID: "list-models", Methods: []string{"GET"}, Paths: []string{"/v1/models"}, Passthrough: true},
				{ID: "create-chat-completion", Methods: []string{"POST"}, Paths: []string{"/v1/chat/completions"}, RequestTextFields: []string{"/messages/*/content", "/messages/*/content/*/text"}, AssistantRoleFields: []string{"/messages/*/role"}, AssistantRoles: []string{"assistant"}, ResponseTextFields: []string{"/choices/*/message/content"}, StreamTextFields: []string{"/choices/*/delta/content"}, StreamChannelFields: []string{"/choices/*/index"}, StreamTerminalData: []string{"[DONE]"}},
				{ID: "create-response", Methods: []string{"POST"}, Paths: []string{"/v1/responses"}, RequestTextFields: []string{"/input", "/input/*/content", "/input/*/content/*/text"}, AssistantRoleFields: []string{"/input/*/role"}, AssistantRoles: []string{"assistant"}, ResponseTextFields: []string{"/output/*/content/*/text"}, StreamTextFields: []string{"/delta"}, StreamChannelFields: []string{"/output_index", "/content_index"}},
			},
		},
		{
			ID: "anthropic", Name: "Anthropic Messages", HeaderTemplates: map[string]string{"x-api-key": "{{api_key}}", "anthropic-version": "2023-06-01"},
			Operations: []Operation{
				{ID: "list-models", Methods: []string{"GET"}, Paths: []string{"/v1/models"}, Passthrough: true},
				{
					ID: "create-message", Methods: []string{"POST"}, Paths: []string{"/v1/messages"},
					RequestTextFields:    []string{"/system", "/system/*/text", "/messages/*/content", "/messages/*/content/*/text"},
					AssistantRoleFields:  []string{"/messages/*/role"},
					AssistantRoles:       []string{"assistant"},
					ResponseTextFields:   []string{"/content/*/text"},
					StreamTextFields:     []string{"/delta/text"},
					StreamChannelFields:  []string{"/index"},
					StreamTerminalEvents: []string{"message_stop"},
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
					AssistantRoleFields: []string{"/contents/*/role"},
					AssistantRoles:      []string{"model"},
					ResponseTextFields:  []string{"/candidates/*/content/parts/*/text"},
					StreamTextFields:    []string{"/candidates/*/content/parts/*/text"},
				},
			},
		},
	}
}

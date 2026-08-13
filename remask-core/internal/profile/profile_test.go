package profile

import "testing"

func TestOpenAIProfileMatchesChatCompletionsAndResponses(t *testing.T) {
	registry := NewRegistry(Builtins()...)
	for _, requestPath := range []string{"/v1/chat/completions", "/v1/responses"} {
		if _, err := registry.Match("openai", "POST", requestPath); err != nil {
			t.Fatalf("OpenAI profile did not match %s: %v", requestPath, err)
		}
	}
}

func TestBuiltinProfilesMatchModelListAsPassthrough(t *testing.T) {
	registry := NewRegistry(Builtins()...)
	requests := []struct {
		profileID string
		path      string
	}{
		{profileID: "openai", path: "/v1/models"},
		{profileID: "anthropic", path: "/v1/models"},
		{profileID: "gemini", path: "/v1beta/models"},
	}
	for _, request := range requests {
		operation, err := registry.Match(request.profileID, "GET", request.path)
		if err != nil {
			t.Fatalf("%s profile did not match %s: %v", request.profileID, request.path, err)
		}
		if operation.ID != "list-models" || !operation.Passthrough {
			t.Fatalf("unexpected %s model-list operation: %#v", request.profileID, operation)
		}
	}
}

func TestBuiltinProfilesExposeAPIKeyHeaderTemplates(t *testing.T) {
	expected := map[string]string{"openai": "Authorization", "anthropic": "x-api-key", "gemini": "x-goog-api-key"}
	registry := NewRegistry(Builtins()...)
	for profileID, header := range expected {
		item, ok := registry.Get(profileID)
		if !ok || item.HeaderTemplates[header] == "" {
			t.Fatalf("profile %q missing header template %q", profileID, header)
		}
	}
}

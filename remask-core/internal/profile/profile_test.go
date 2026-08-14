package profile

import (
	"testing"
)

func TestOpenAIProfileMatchesChatCompletionsAndResponses(t *testing.T) {
	registry := NewRegistry(Builtins()...)
	for _, requestPath := range []string{"/v1/chat/completions", "/v1/responses"} {
		if _, err := registry.Match("openai", "POST", requestPath); err != nil {
			t.Fatalf("OpenAI profile did not match %s: %v", requestPath, err)
		}
	}
}

func TestCodexChatGPTProfileMatchesKnownResponsesPaths(t *testing.T) {
	registry := NewRegistry(Builtins()...)
	for _, requestPath := range []string{"/backend-api/codex/responses", "/backend-api/api/codex", "/backend-api/api/codex/responses/compact"} {
		if _, err := registry.Match("codex-chatgpt", "POST", requestPath); err != nil {
			t.Fatalf("Codex ChatGPT profile did not match %s: %v", requestPath, err)
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

func TestGenericMatchRecognizesModelRequestShapes(t *testing.T) {
	for _, body := range []string{
		`{"model":"custom-model","messages":[{"role":"user","content":"hello"}]}`,
		`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`,
		`{"input":"hello"}`,
	} {
		operation, err := GenericMatch("POST", []byte(body))
		if err != nil || operation.ID != "generic-model-request" {
			t.Fatalf("generic match failed for %s: operation=%#v err=%v", body, operation, err)
		}
	}
	if _, err := GenericMatch("GET", []byte(`{"model":"custom-model"}`)); err == nil {
		t.Fatal("GET request should not use generic strategy")
	}
	if _, err := GenericMatch("POST", []byte(`{"payload":"ordinary request"}`)); err == nil {
		t.Fatal("ordinary POST should not use generic strategy")
	}
}

func TestGenericOperationCombinesProviderFields(t *testing.T) {
	operation := GenericOperation()
	for _, selector := range []string{
		"/messages/*/content", "/system", "/input", "/contents/*/parts/*/text",
		"/choices/*/message/content", "/content/*/text", "/candidates/*/content/parts/*/text",
	} {
		if !contains(operation.RequestTextFields, selector) && !contains(operation.ResponseTextFields, selector) {
			t.Fatalf("generic operation missing provider selector %q", selector)
		}
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

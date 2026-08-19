package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureExampleFileInitializesFreshProfilesDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "profiles")
	if err := EnsureExampleFile(directory); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, ExampleFileName))
	if err != nil {
		t.Fatal(err)
	}
	var example Profile
	if err := json.Unmarshal(data, &example); err != nil {
		t.Fatal(err)
	}
	if example.ID != "example-openai-compatible" || len(example.Operations) == 0 || example.HeaderTemplates["Authorization"] == "" {
		t.Fatalf("incomplete example profile: %#v", example)
	}
	registry := NewRegistry(Builtins()...)
	if err := registry.LoadDir(directory); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get(example.ID); !ok {
		t.Fatal("initialized example was not available in the registry")
	}
}

func TestEnsureExampleFileDoesNotModifyExistingProfilesDirectory(t *testing.T) {
	directory := t.TempDir()
	existingPath := filepath.Join(directory, "custom.json")
	existing := []byte(`{"id":"custom","name":"Custom","operations":[]}`)
	if err := os.WriteFile(existingPath, existing, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureExampleFile(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, ExampleFileName)); !os.IsNotExist(err) {
		t.Fatalf("example should not be added beside an existing profile: %v", err)
	}
	unchanged, err := os.ReadFile(existingPath)
	if err != nil || string(unchanged) != string(existing) {
		t.Fatalf("existing profile changed: %v", err)
	}
}

func TestEnsureExampleFileDoesNotRepopulateExistingEmptyDirectory(t *testing.T) {
	directory := t.TempDir()
	if err := EnsureExampleFile(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, ExampleFileName)); !os.IsNotExist(err) {
		t.Fatalf("example should only be created with the directory: %v", err)
	}
}

func TestRegistryLoadsProfilesFromDirectory(t *testing.T) {
	directory := t.TempDir()
	data := []byte(`{"id":"custom","name":"Custom API","operations":[{"id":"chat","methods":["POST"],"paths":["/chat"],"request_text_fields":["/prompt"],"response_text_fields":["/answer"]}]}`)
	if err := os.WriteFile(filepath.Join(directory, "custom.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(Builtins()...)
	if err := registry.LoadDir(directory); err != nil {
		t.Fatal(err)
	}
	loaded, ok := registry.Get("custom")
	if !ok || loaded.Name != "Custom API" {
		t.Fatalf("custom profile not loaded: %#v", loaded)
	}
	if _, err := registry.Match("custom", "POST", "/chat"); err != nil {
		t.Fatalf("custom profile did not match: %v", err)
	}
}

func TestRegistryUserProfileOverridesBuiltin(t *testing.T) {
	directory := t.TempDir()
	data := []byte(`{"profiles":[{"id":"openai","name":"Local OpenAI","operations":[]}]}`)
	if err := os.WriteFile(filepath.Join(directory, "override.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(Builtins()...)
	if err := registry.LoadDir(directory); err != nil {
		t.Fatal(err)
	}
	loaded, _ := registry.Get("openai")
	if loaded.Name != "Local OpenAI" {
		t.Fatalf("built-in profile was not overridden: %#v", loaded)
	}
}

func TestRegistryRefreshRemovesDeletedUserProfiles(t *testing.T) {
	directory := t.TempDir()
	profilePath := filepath.Join(directory, "custom.json")
	if err := os.WriteFile(profilePath, []byte(`{"id":"custom","name":"Custom","operations":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(Builtins()...)
	if err := registry.LoadDir(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(profilePath); err != nil {
		t.Fatal(err)
	}
	if err := registry.Refresh(); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("custom"); ok {
		t.Fatal("deleted custom profile remained in registry")
	}
	if _, ok := registry.Get("openai"); !ok {
		t.Fatal("refresh removed built-in profiles")
	}
}

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

func TestGenericProfileCombinesBuiltinProviderOperations(t *testing.T) {
	registry := NewRegistry(Builtins()...)
	requests := []struct {
		method string
		path   string
		wantID string
	}{
		{method: "POST", path: "/v1/chat/completions", wantID: "create-chat-completion"},
		{method: "POST", path: "/v1/responses", wantID: "create-response"},
		{method: "POST", path: "/v1/messages", wantID: "create-message"},
		{method: "POST", path: "/v1beta/models/gemini-pro:generateContent", wantID: "generate-content"},
		{method: "POST", path: "/backend-api/codex/responses", wantID: "create-codex-response"},
	}
	for _, request := range requests {
		operation, err := registry.Match("generic", request.method, request.path)
		if err != nil || operation.ID != request.wantID {
			t.Errorf("generic profile match %s: operation=%#v err=%v", request.path, operation, err)
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

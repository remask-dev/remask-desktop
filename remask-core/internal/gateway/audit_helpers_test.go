package gateway

import "testing"

func TestMaskValueKeepsSafeEdges(t *testing.T) {
	if got := maskValue("13800138000"); got != "138***000" {
		t.Fatalf("phone mask = %q", got)
	}
	if got := maskValue("foo@example.com"); got != "foo***com" {
		t.Fatalf("email mask = %q", got)
	}
}

func TestExtractTokenUsageAcrossProviders(t *testing.T) {
	for name, body := range map[string]string{
		"openai":    `{"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20,"prompt_tokens_details":{"cached_tokens":6}}}`,
		"anthropic": `{"message":{"usage":{"input_tokens":7,"output_tokens":5}}}`,
		"gemini":    `{"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":4,"totalTokenCount":13}}`,
		"deepseek":  `{"usage":{"prompt_tokens":15,"completion_tokens":3,"total_tokens":18,"prompt_cache_hit_tokens":5,"prompt_cache_miss_tokens":10}}`,
	} {
		usage := extractTokenUsage([]byte(body))
		if usage.Total == 0 {
			t.Fatalf("%s usage not extracted: %#v", name, usage)
		}
		if name == "deepseek" && usage.Cached != 5 {
			t.Fatalf("DeepSeek cached usage not extracted: %#v", usage)
		}
	}
	openAI := extractTokenUsage([]byte(`{"usage":{"input_tokens":12,"input_tokens_details":{"cached_tokens":6}}}`))
	if openAI.Cached != 6 {
		t.Fatalf("OpenAI cached usage not extracted: %#v", openAI)
	}
}

func TestExtractTokenUsageFromSSE(t *testing.T) {
	body := "data: {\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n"
	usage := extractTokenUsage([]byte(body))
	if usage.Input != 3 || usage.Output != 2 || usage.Total != 5 {
		t.Fatalf("usage = %#v", usage)
	}
}

func TestExtractRequestModel(t *testing.T) {
	if got := extractModelFromBody([]byte(`{"model":"gpt-4.1","messages":[]}`)); got != "gpt-4.1" {
		t.Fatalf("body model = %q", got)
	}
	if got := extractModelFromBody([]byte(`{"model":42}`)); got != "" {
		t.Fatalf("non-string body model = %q", got)
	}
	for path, want := range map[string]string{
		"/v1beta/models/gemini-2.0-flash:generateContent":                                "gemini-2.0-flash",
		"/v1beta/models/publishers/google/models/gemini-2.0-flash:streamGenerateContent": "gemini-2.0-flash",
		"/v1/models": "",
	} {
		if got := extractModelFromPath(path); got != want {
			t.Fatalf("path %q model = %q, want %q", path, got, want)
		}
	}
}

func TestTargetHostname(t *testing.T) {
	if got := targetHostname("https://API.Example.com:8443/v1"); got != "api.example.com" {
		t.Fatalf("target hostname = %q", got)
	}
}

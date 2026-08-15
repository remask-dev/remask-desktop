//go:build !noonnxruntime

package model

import (
	"strings"
	"testing"

	ort "github.com/yalue/onnxruntime_go"
)

func TestNormalizeProvider(t *testing.T) {
	tests := map[string]string{
		"":         "auto",
		" GPU ":    "gpu",
		"dml":      "directml",
		"Apple":    "coreml",
		"trt":      "tensorrt",
		"cuda":     "cuda",
		"openvino": "openvino",
	}
	for input, expected := range tests {
		got, err := normalizeProvider(input)
		if err != nil {
			t.Fatalf("normalizeProvider(%q): %v", input, err)
		}
		if got != expected {
			t.Errorf("normalizeProvider(%q) = %q, want %q", input, got, expected)
		}
	}
	if _, err := normalizeProvider("not-a-provider"); err == nil {
		t.Fatal("normalizeProvider accepted an unknown provider")
	}
}

func TestProviderCandidatesExplicit(t *testing.T) {
	runtime := onnxRuntime{provider: "cuda", deviceID: 2}
	candidates := runtime.providerCandidates()
	if len(candidates) != 1 || candidates[0] != "cuda" {
		t.Fatalf("explicit provider candidates = %#v, want [cuda]", candidates)
	}
}

func TestProviderCandidatesAutoIncludesCPUFallback(t *testing.T) {
	runtime := onnxRuntime{provider: "auto"}
	candidates := runtime.providerCandidates()
	if len(candidates) == 0 || candidates[len(candidates)-1] != "cpu" {
		t.Fatalf("auto provider candidates = %#v, want CPU fallback", candidates)
	}
	if strings.Join(candidates, ",") == "cpu" {
		t.Skip("current platform has no configured GPU provider candidate")
	}
}

func TestReconcileTokenTypeIDsUsesGraphAsAuthority(t *testing.T) {
	tensor := func(name string) ort.InputOutputInfo {
		return ort.InputOutputInfo{Name: name, OrtValueType: ort.ONNXTypeTensor}
	}
	tests := []struct {
		name       string
		configured string
		inputs     []ort.InputOutputInfo
		want       string
	}{
		{
			name:       "removes inferred input missing from optimized graph",
			configured: "token_type_ids",
			inputs:     []ort.InputOutputInfo{tensor("input_ids"), tensor("attention_mask")},
		},
		{
			name:   "adds input declared by graph",
			inputs: []ort.InputOutputInfo{tensor("input_ids"), tensor("attention_mask"), tensor("token_type_ids")},
			want:   "token_type_ids",
		},
		{
			name:       "keeps custom manifest input for strict validation",
			configured: "segment_ids",
			inputs:     []ort.InputOutputInfo{tensor("input_ids"), tensor("attention_mask")},
			want:       "segment_ids",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := Manifest{Inputs: InputSpec{TokenTypeIDs: test.configured}}
			reconcileTokenTypeIDs(&manifest, test.inputs)
			if got := manifest.Inputs.TokenTypeIDs; got != test.want {
				t.Fatalf("token_type_ids = %q, want %q", got, test.want)
			}
		})
	}
}

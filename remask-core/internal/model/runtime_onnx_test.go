//go:build !noonnxruntime

package model

import (
	"strings"
	"testing"
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

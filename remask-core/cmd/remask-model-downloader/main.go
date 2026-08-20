package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/remask-dev/remask-core/internal/modeldownload"
)

func main() {
	repo := flag.String("repo", "openai/privacy-filter", "Hugging Face repository or project URL")
	revision := flag.String("revision", "main", "Hugging Face branch, tag, or commit")
	variant := flag.String("variant", "q4f16", "ONNX quantization variant, for example q4f16 or q4")
	baseURL := flag.String("base-url", envOr("REMASK_HF_BASE_URL", "https://huggingface.co"), "Hugging Face endpoint or mirror")
	output := flag.String("output", "models", "model root directory")
	id := flag.String("id", "openai-privacy-filter-q4f16", "model package ID")
	name := flag.String("name", "OpenAI Privacy Filter Q4F16", "display name")
	token := flag.String("token", "", "optional Hugging Face access token (prefer HF_TOKEN)")
	flag.Parse()
	if *token == "" {
		*token = os.Getenv("HF_TOKEN")
	}
	directory, err := modeldownload.Download(context.Background(), modeldownload.Config{Root: *output, ID: *id, Name: *name, Repo: *repo, Revision: *revision, Variant: *variant, BaseURL: *baseURL, Token: *token})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("Downloaded %s (%s) to %s\n", *repo, *variant, directory)
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

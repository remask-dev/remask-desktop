package modeldownload

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/remask/remask-core/internal/model"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestSelectFilesPreservesExternalDataBasename(t *testing.T) {
	files, err := selectFiles(map[string]bool{
		"onnx/model_q4f16.onnx":      true,
		"onnx/model_q4f16.onnx_data": true,
		"tokenizer.json":             true,
	}, "q4f16")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.key == "model_data" {
			if file.local != "model_q4f16.onnx_data" {
				t.Fatalf("external data local path = %q, want model_q4f16.onnx_data", file.local)
			}
			return
		}
	}
	t.Fatal("selectFiles did not return an external data file")
}

func TestDownloadDoesNotInferTokenTypeIDsFromArchitectureConfig(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status := http.StatusOK
		body := ""
		switch request.URL.Path {
		case "/api/models/example/pii":
			data, err := json.Marshal(map[string]any{
				"sha": "revision",
				"siblings": []map[string]string{
					{"rfilename": "onnx/model.onnx"},
					{"rfilename": "vocab.txt"},
					{"rfilename": "config.json"},
				},
			})
			if err != nil {
				return nil, err
			}
			body = string(data)
		case "/example/pii/resolve/main/onnx/model.onnx":
			body = "test model"
		case "/example/pii/resolve/main/vocab.txt":
			body = "[PAD]\n[UNK]\n[CLS]\n[SEP]\n"
		case "/example/pii/resolve/main/config.json":
			body = `{"type_vocab_size":2,"id2label":{"0":"O","1":"B-NAME"}}`
		default:
			status = http.StatusNotFound
		}
		return &http.Response{
			StatusCode: status,
			Status:     http.StatusText(status),
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}

	root := t.TempDir()
	directory, err := Download(context.Background(), Config{
		Root: root, ID: "example-pii", Repo: "example/pii", BaseURL: "https://models.example", HTTPClient: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest model.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Inputs.TokenTypeIDs != "" {
		t.Fatalf("download inferred token_type_ids = %q from source config", manifest.Inputs.TokenTypeIDs)
	}
}

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/remask/remask-core/internal/model"
)

func main() {
	directory := flag.String("dir", "", "model package directory")
	id := flag.String("id", "", "model package id")
	name := flag.String("name", "", "display name")
	version := flag.String("version", "", "upstream model revision")
	quantization := flag.String("quantization", "q4", "model quantization")
	modelFile := flag.String("model", "model_q4.onnx", "model filename")
	vocabFile := flag.String("vocab", "vocab.txt", "WordPiece vocabulary filename")
	configFile := flag.String("config", "config.json", "transformers config containing id2label")
	tokenizerType := flag.String("tokenizer-type", "bert-wordpiece", "tokenizer type: bert-wordpiece or o200k-base")
	labelScheme := flag.String("label-scheme", "BIO", "label scheme: BIO or BIOES")
	decoderType := flag.String("decoder", "argmax", "decoder type: argmax or viterbi-bioes")
	calibrationFile := flag.String("calibration", "", "optional decoder calibration filename")
	operatingPoint := flag.String("operating-point", "", "optional decoder operating point")
	extraFiles := flag.String("extra-files", "", "comma-separated manifest-key=relative-path entries")
	entityTypes := flag.String("entity-types", "", "comma-separated raw=normalized entity mappings")
	maxTokens := flag.Int("max-tokens", 512, "maximum sequence length")
	stride := flag.Int("stride", 64, "overlap between token windows")
	inputIDs := flag.String("input-ids", "input_ids", "input_ids tensor name")
	attentionMask := flag.String("attention-mask", "attention_mask", "attention_mask tensor name")
	logits := flag.String("logits", "logits", "logits tensor name")
	flag.Parse()

	if *directory == "" || *id == "" || *name == "" || *version == "" {
		fatalf("dir, id, name, and version are required")
	}
	labels, err := labelsFromConfig(filepath.Join(*directory, *configFile))
	if err != nil {
		fatalf("read labels: %v", err)
	}
	labelsPath := filepath.Join(*directory, "labels.json")
	if err := writeJSON(labelsPath, labels); err != nil {
		fatalf("write labels: %v", err)
	}

	files := make(map[string]model.FileSpec)
	packageFiles := map[string]string{"model": *modelFile, "tokenizer": *vocabFile, "labels": "labels.json"}
	for _, entry := range splitEntries(*extraFiles) {
		key, filename, ok := strings.Cut(entry, "=")
		if !ok || key == "" || filename == "" {
			fatalf("invalid extra file %q", entry)
		}
		packageFiles[key] = filename
	}
	for key, filename := range packageFiles {
		spec, err := fileSpec(filepath.Join(*directory, filename), filename)
		if err != nil {
			fatalf("inspect %s: %v", filename, err)
		}
		files[key] = spec
	}
	manifest := model.Manifest{
		SchemaVersion: 1, ID: *id, Name: *name, Version: *version,
		Task: "token-classification", Quantization: *quantization, LabelScheme: *labelScheme,
		MaxTokens: *maxTokens, Stride: *stride, Files: files,
		Inputs:  model.InputSpec{InputIDs: *inputIDs, AttentionMask: *attentionMask},
		Outputs: model.OutputSpec{Logits: *logits},
		Tokenizer: model.TokenizerSpec{
			Type: *tokenizerType, LowerCase: *tokenizerType == "bert-wordpiece", StripAccents: *tokenizerType == "bert-wordpiece", TokenizeChineseChars: *tokenizerType == "bert-wordpiece",
			UnknownToken: "[UNK]", ClassificationToken: "[CLS]", SeparatorToken: "[SEP]", PaddingToken: "[PAD]",
		},
		Decoder:           model.DecoderSpec{Type: *decoderType, Calibration: *calibrationFile, OperatingPoint: *operatingPoint},
		EntityTypes:       parseMappings(*entityTypes),
		MinimumConfidence: map[string]float64{"*": 0.55},
		SelfTestText:      "Contact John Smith at john.smith@example.com",
	}
	if err := writeJSON(filepath.Join(*directory, "manifest.json"), manifest); err != nil {
		fatalf("write manifest: %v", err)
	}
}

func splitEntries(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func parseMappings(value string) map[string]string {
	result := make(map[string]string)
	for _, entry := range splitEntries(value) {
		raw, normalized, ok := strings.Cut(entry, "=")
		if !ok || raw == "" || normalized == "" {
			fatalf("invalid entity type mapping %q", entry)
		}
		result[raw] = normalized
	}
	return result
}

func labelsFromConfig(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config struct {
		IDToLabel map[string]string `json:"id2label"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	if len(config.IDToLabel) == 0 {
		return nil, fmt.Errorf("config does not contain id2label")
	}
	return config.IDToLabel, nil
}

func fileSpec(path, relative string) (model.FileSpec, error) {
	file, err := os.Open(path)
	if err != nil {
		return model.FileSpec{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return model.FileSpec{}, err
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return model.FileSpec{}, err
	}
	return model.FileSpec{Path: relative, Size: info.Size(), SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

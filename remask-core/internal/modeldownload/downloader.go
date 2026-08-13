package modeldownload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/remask/remask-core/internal/model"
)

type Config struct {
	Root       string
	ID         string
	Name       string
	Repo       string
	Revision   string
	Variant    string
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

type remoteFile struct{ key, remote, local string }

type repoInfo struct {
	Siblings []struct {
		Name string `json:"rfilename"`
	} `json:"siblings"`
}

// Download writes a complete, checksummed Remask model package. It is safe to
// retry: each large object is first written to a .part file and supports Range.
func Download(ctx context.Context, cfg Config) (string, error) {
	if cfg.Root == "" || cfg.ID == "" || cfg.Repo == "" || cfg.Variant == "" {
		return "", errors.New("root, id, repo, and variant are required")
	}
	if parsed, err := url.Parse(cfg.Repo); err == nil && parsed.IsAbs() {
		if parsed.Host == "" || parsed.Path == "" {
			return "", errors.New("repo URL is invalid")
		}
		cfg.Repo = strings.Trim(parsed.Path, "/")
		if strings.HasSuffix(cfg.Repo, ".git") {
			cfg.Repo = strings.TrimSuffix(cfg.Repo, ".git")
		}
	}
	if strings.ContainsAny(cfg.ID, `/\\`) || strings.ContainsAny(cfg.Variant, `/\\`) {
		return "", errors.New("id and variant must not contain path separators")
	}
	if cfg.Revision == "" {
		cfg.Revision = "main"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://huggingface.co"
	}
	if cfg.Name == "" {
		cfg.Name = cfg.ID
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Minute}
	}
	directory := filepath.Join(cfg.Root, cfg.ID)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	available, err := listRepoFiles(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("list repository files: %w", err)
	}
	files, err := selectFiles(available, cfg.Variant)
	if err != nil {
		return "", err
	}
	for _, file := range files {
		url := strings.TrimRight(cfg.BaseURL, "/") + "/" + strings.Trim(cfg.Repo, "/") + "/resolve/" + cfg.Revision + "/" + file.remote
		if err := downloadFile(ctx, cfg.HTTPClient, url, filepath.Join(directory, file.local), cfg.Token); err != nil {
			return "", fmt.Errorf("download %s: %w", file.remote, err)
		}
	}
	labels, err := labelsFromConfig(filepath.Join(directory, "config.json"))
	if err != nil {
		return "", fmt.Errorf("read config labels: %w", err)
	}
	if err := writeJSON(filepath.Join(directory, "labels.json"), labels); err != nil {
		return "", err
	}
	maxTokens, stride := modelSequenceConfig(filepath.Join(directory, "config.json"), tokenizerTypeForFiles(files))
	tokenTypeIDs := ""
	if configUsesTokenTypeIDs(filepath.Join(directory, "config.json")) {
		tokenTypeIDs = "token_type_ids"
	}
	specs := make(map[string]model.FileSpec, len(files)+1)
	for _, file := range files {
		spec, err := fileSpec(filepath.Join(directory, file.local), file.local)
		if err != nil {
			return "", err
		}
		specs[file.key] = spec
	}
	labelSpec, err := fileSpec(filepath.Join(directory, "labels.json"), "labels.json")
	if err != nil {
		return "", err
	}
	specs["labels"] = labelSpec
	labelScheme := "BIO"
	decoderType := "argmax"
	if hasBIOES(labels) {
		labelScheme = "BIOES"
		decoderType = "viterbi-bioes"
	}
	tokenizerType := "bert-wordpiece"
	if _, ok := specs["tokenizer"]; !ok {
		return "", errors.New("repository does not contain tokenizer.json or vocab.txt")
	}
	if specs["tokenizer"].Path == "vocab.txt" {
		// WordPiece models use a regular B/I head and do not need o200k metadata.
		tokenizerType = "bert-wordpiece"
	} else if tokenizerJSONLooksO200k(filepath.Join(directory, specs["tokenizer"].Path)) {
		tokenizerType = "o200k-base"
	}
	manifest := model.Manifest{
		SchemaVersion: 1, ID: cfg.ID, Name: cfg.Name, Version: cfg.Revision, Task: "token-classification", Quantization: cfg.Variant, LabelScheme: labelScheme, MaxTokens: maxTokens, Stride: stride, Files: specs,
		Inputs: model.InputSpec{InputIDs: "input_ids", AttentionMask: "attention_mask", TokenTypeIDs: tokenTypeIDs}, Outputs: model.OutputSpec{Logits: "logits"},
		Tokenizer:   model.TokenizerSpec{Type: tokenizerType, LowerCase: tokenizerType == "bert-wordpiece", StripAccents: tokenizerType == "bert-wordpiece", TokenizeChineseChars: tokenizerType == "bert-wordpiece", UnknownToken: "[UNK]", ClassificationToken: "[CLS]", SeparatorToken: "[SEP]", PaddingToken: "[PAD]"},
		Decoder:     model.DecoderSpec{Type: decoderType, OperatingPoint: "default"},
		EntityTypes: entityTypesFromLabels(labels), MinimumConfidence: map[string]float64{"*": 0.55}, SelfTestText: "Contact John Smith at john.smith@example.com",
	}
	if _, ok := specs["calibration"]; ok {
		manifest.Decoder.Calibration = "viterbi_calibration.json"
	}
	if err := writeJSON(filepath.Join(directory, "manifest.json"), manifest); err != nil {
		return "", err
	}
	return directory, nil
}

func tokenizerTypeForFiles(files []remoteFile) string {
	for _, file := range files {
		if file.key == "tokenizer" && file.local == "tokenizer.json" {
			return "o200k-base"
		}
	}
	return "bert-wordpiece"
}

func modelSequenceConfig(path, tokenizerType string) (int, int) {
	maxTokens := 512
	if data, err := os.ReadFile(path); err == nil {
		var config struct {
			MaxPositionEmbeddings int `json:"max_position_embeddings"`
		}
		if json.Unmarshal(data, &config) == nil && config.MaxPositionEmbeddings >= 3 {
			maxTokens = config.MaxPositionEmbeddings
		}
	}
	stride := 64
	if tokenizerType == "o200k-base" {
		stride = 128
	}
	if stride >= maxTokens-2 {
		stride = maxTokens / 4
	}
	if stride < 0 {
		stride = 0
	}
	return maxTokens, stride
}

func configUsesTokenTypeIDs(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var config struct {
		TypeVocabSize int `json:"type_vocab_size"`
	}
	if json.Unmarshal(data, &config) != nil {
		return false
	}
	return config.TypeVocabSize > 1
}

func downloadFile(ctx context.Context, client *http.Client, url, destination, token string) error {
	temporary := destination + ".part"
	var offset int64
	if info, err := os.Stat(temporary); err == nil {
		offset = info.Size()
	} else if !os.IsNotExist(err) {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "remask-model-downloader/1.0")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	if offset > 0 && resp.StatusCode != http.StatusPartialContent {
		offset = 0
	}
	flags := os.O_CREATE | os.O_WRONLY
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(temporary, flags, 0o644)
	if err != nil {
		return err
	}
	if offset > 0 {
		if _, err = file.Seek(offset, io.SeekStart); err != nil {
			file.Close()
			return err
		}
	}
	_, copyErr := io.Copy(file, resp.Body)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(temporary, destination)
}

func listRepoFiles(ctx context.Context, cfg Config) (map[string]bool, error) {
	endpoint := strings.TrimRight(cfg.BaseURL, "/") + "/api/models/" + strings.Trim(cfg.Repo, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "remask-model-downloader/1.0")
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	resp, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	var info repoInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	files := make(map[string]bool, len(info.Siblings))
	for _, item := range info.Siblings {
		files[item.Name] = true
	}
	return files, nil
}

func selectFiles(available map[string]bool, variant string) ([]remoteFile, error) {
	modelRemote := ""
	preferred := []string{"onnx/model_" + variant + ".onnx", "onnx/model_q4.onnx", "onnx/model.onnx", "model.onnx"}
	for _, candidate := range preferred {
		if available[candidate] {
			modelRemote = candidate
			break
		}
	}
	if modelRemote == "" {
		for name := range available {
			if strings.HasSuffix(name, ".onnx") && strings.Contains(name, "/") {
				modelRemote = name
				break
			}
		}
	}
	if modelRemote == "" {
		return nil, errors.New("repository does not contain an ONNX model file")
	}
	files := []remoteFile{{key: "model", remote: modelRemote, local: "model.onnx"}}
	if available[modelRemote+"_data"] {
		files = append(files, remoteFile{key: "model_data", remote: modelRemote + "_data", local: "model.onnx_data"})
	}
	// A repository may publish both files. vocab.txt is the native input for
	// the Go WordPiece tokenizer; tokenizer.json is used for BPE/o200k models.
	if available["vocab.txt"] {
		files = append(files, remoteFile{key: "tokenizer", remote: "vocab.txt", local: "vocab.txt"})
	} else if available["tokenizer.json"] {
		files = append(files, remoteFile{key: "tokenizer", remote: "tokenizer.json", local: "tokenizer.json"})
	} else {
		return nil, errors.New("repository does not contain tokenizer.json or vocab.txt")
	}
	for _, name := range []string{"config.json", "tokenizer_config.json", "special_tokens_map.json", "viterbi_calibration.json"} {
		if available[name] {
			key := strings.TrimSuffix(name, ".json")
			if name == "viterbi_calibration.json" {
				key = "calibration"
			}
			files = append(files, remoteFile{key: key, remote: name, local: name})
		}
	}
	return files, nil
}

func hasBIOES(labels map[string]string) bool {
	for _, label := range labels {
		if strings.HasPrefix(label, "E-") || strings.HasPrefix(label, "S-") {
			return true
		}
	}
	return false
}

// entityTypesFromLabels maps known model labels to the canonical OpenAI entity
// categories used by Remask, while retaining unknown labels for display and
// policy control instead of silently dropping them.
func entityTypesFromLabels(labels map[string]string) map[string]string {
	result := make(map[string]string)
	for _, label := range labels {
		base := label
		if index := strings.IndexByte(base, '-'); index >= 0 {
			base = base[index+1:]
		}
		if base == "" || strings.EqualFold(base, "O") {
			continue
		}
		raw := strings.ToUpper(base)
		value := raw
		if normalized := model.CanonicalEntityType(raw); normalized != "" {
			value = normalized
		}
		result[raw] = value
	}
	return result
}

func tokenizerJSONLooksO200k(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var tokenizer struct {
		Model struct {
			Type         string `json:"type"`
			IgnoreMerges bool   `json:"ignore_merges"`
		} `json:"model"`
	}
	if json.Unmarshal(data, &tokenizer) != nil {
		return false
	}
	return tokenizer.Model.Type == "BPE" && tokenizer.Model.IgnoreMerges
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
		return nil, errors.New("config does not contain id2label")
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
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return model.FileSpec{}, err
	}
	return model.FileSpec{Path: relative, Size: info.Size(), SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}
func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

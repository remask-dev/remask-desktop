package model

import (
	"context"
	"errors"
	"time"

	"github.com/remask/remask-core/internal/pii"
)

var (
	ErrNotFound           = errors.New("model not found")
	ErrRuntimeUnavailable = errors.New("ONNX Runtime is not available in this build")
)

// MaxInferenceTokens bounds the amount of text passed to a local model in one
// ONNX invocation. Some model configs advertise a very large context window
// (for example 128K), but allocating tensors of that size is unsafe for a
// desktop privacy filter and can exhaust system memory. Long input is still
// processed through overlapping windows.
const MaxInferenceTokens = 4096

type FileSpec struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

type InputSpec struct {
	InputIDs      string `json:"input_ids"`
	AttentionMask string `json:"attention_mask"`
	TokenTypeIDs  string `json:"token_type_ids,omitempty"`
}

type OutputSpec struct {
	Logits string `json:"logits"`
}

type TokenizerSpec struct {
	Type                 string `json:"type,omitempty"`
	LowerCase            bool   `json:"lower_case,omitempty"`
	StripAccents         bool   `json:"strip_accents,omitempty"`
	TokenizeChineseChars bool   `json:"tokenize_chinese_chars,omitempty"`
	UnknownToken         string `json:"unknown_token,omitempty"`
	ClassificationToken  string `json:"classification_token,omitempty"`
	SeparatorToken       string `json:"separator_token,omitempty"`
	PaddingToken         string `json:"padding_token,omitempty"`
}

type DecoderSpec struct {
	Type           string `json:"type,omitempty"`
	Calibration    string `json:"calibration_file,omitempty"`
	OperatingPoint string `json:"operating_point,omitempty"`
}

type Manifest struct {
	SchemaVersion     int                 `json:"schema_version"`
	ID                string              `json:"id"`
	Name              string              `json:"name"`
	Version           string              `json:"version"`
	Task              string              `json:"task"`
	Quantization      string              `json:"quantization,omitempty"`
	LabelScheme       string              `json:"label_scheme"`
	MaxTokens         int                 `json:"max_tokens"`
	Stride            int                 `json:"stride"`
	Files             map[string]FileSpec `json:"files"`
	Inputs            InputSpec           `json:"inputs"`
	Outputs           OutputSpec          `json:"outputs"`
	Tokenizer         TokenizerSpec       `json:"tokenizer_config,omitempty"`
	Decoder           DecoderSpec         `json:"decoder,omitempty"`
	Labels            map[string]string   `json:"labels,omitempty"`
	EntityTypes       map[string]string   `json:"entity_types,omitempty"`
	MinimumConfidence map[string]float64  `json:"minimum_confidence,omitempty"`
	SelfTestText      string              `json:"self_test_text,omitempty"`
}

type Metadata struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	Runtime      string `json:"runtime"`
	Quantization string `json:"quantization,omitempty"`
}

type Package struct {
	ID        string    `json:"id"`
	Path      string    `json:"-"`
	Manifest  Manifest  `json:"manifest"`
	Valid     bool      `json:"valid"`
	Errors    []string  `json:"errors"`
	Active    bool      `json:"active"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Session interface {
	pii.Detector
	Metadata() Metadata
	Close() error
}

type Runtime interface {
	Name() string
	Available() bool
	Load(ctx context.Context, packagePath string, manifest Manifest) (Session, error)
}

// RuntimeOptions controls the ONNX Runtime execution provider. Provider
// "auto" selects the best GPU provider for the current platform and falls
// back to CPU when that provider is unavailable.
type RuntimeOptions struct {
	Provider string
	DeviceID int
}

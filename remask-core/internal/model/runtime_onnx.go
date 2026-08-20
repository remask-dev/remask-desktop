//go:build !noonnxruntime

package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/remask-dev/remask-core/internal/pii"
)

type onnxRuntime struct {
	version        string
	provider       string
	deviceID       int
	activeProvider string
	providerMu     sync.RWMutex
}

type onnxSession struct {
	metadata   Metadata
	manifest   Manifest
	tokenizer  textTokenizer
	labels     []string
	session    *ort.DynamicAdvancedSession
	runMu      sync.Mutex
	closeOnce  sync.Once
	closeError error
}

func NewRuntime(libraryPath string) (Runtime, error) {
	deviceID := 0
	if value := os.Getenv("REMASK_ONNX_DEVICE"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			deviceID = parsed
		}
	}
	return NewRuntimeWithOptions(libraryPath, RuntimeOptions{
		Provider: os.Getenv("REMASK_ONNX_PROVIDER"),
		DeviceID: deviceID,
	})
}

func NewRuntimeWithOptions(libraryPath string, options RuntimeOptions) (Runtime, error) {
	if libraryPath == "" {
		libraryPath = os.Getenv("REMASK_ONNXRUNTIME_LIBRARY")
	}
	provider, err := normalizeProvider(options.Provider)
	if err != nil {
		return nil, err
	}
	if options.DeviceID < 0 {
		return nil, fmt.Errorf("ONNX device ID must be non-negative, got %d", options.DeviceID)
	}
	if libraryPath != "" {
		ort.SetSharedLibraryPath(libraryPath)
	}
	if err := ort.InitializeEnvironment(ort.WithLogLevelWarning()); err != nil {
		return nil, fmt.Errorf("initialize ONNX Runtime: %w", err)
	}
	_ = ort.DisableTelemetry()
	return &onnxRuntime{version: ort.GetVersion(), provider: provider, deviceID: options.DeviceID}, nil
}

func (r *onnxRuntime) Name() string    { return "onnxruntime-" + r.version }
func (r *onnxRuntime) Available() bool { return true }
func (r *onnxRuntime) Provider() string {
	r.providerMu.RLock()
	defer r.providerMu.RUnlock()
	if r.activeProvider != "" {
		return r.activeProvider
	}
	return r.provider
}

func (r *onnxRuntime) ConfiguredProvider() string {
	r.providerMu.RLock()
	defer r.providerMu.RUnlock()
	return r.provider
}

func (r *onnxRuntime) ActiveProvider() string {
	r.providerMu.RLock()
	defer r.providerMu.RUnlock()
	return r.activeProvider
}

// SetProvider changes the provider used when the next model Session is loaded.
// Existing Sessions keep their current execution provider until reloaded.
func (r *onnxRuntime) SetProvider(value string) error {
	provider, err := normalizeProvider(value)
	if err != nil {
		return err
	}
	r.providerMu.Lock()
	r.provider = provider
	r.providerMu.Unlock()
	return nil
}

func (r *onnxRuntime) Load(ctx context.Context, packagePath string, manifest Manifest) (Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	modelPath, err := secureModelPath(packagePath, manifest.Files["model"].Path)
	if err != nil {
		return nil, err
	}
	tokenizerPath, err := secureModelPath(packagePath, manifest.Files["tokenizer"].Path)
	if err != nil {
		return nil, err
	}
	labelsPath, err := secureModelPath(packagePath, manifest.Files["labels"].Path)
	if err != nil {
		return nil, err
	}

	tokenizer, err := loadTokenizer(tokenizerPath, manifest.Tokenizer)
	if err != nil {
		return nil, fmt.Errorf("load tokenizer: %w", err)
	}
	labels, err := loadLabels(labelsPath, manifest.Labels)
	if err != nil {
		return nil, fmt.Errorf("load labels: %w", err)
	}
	// The exported graph is authoritative for optional BERT segment IDs.
	// Optimized exports may omit token_type_ids even when the source model's
	// config advertises segment embeddings.
	if inputs, _, infoErr := ort.GetInputOutputInfo(modelPath); infoErr == nil {
		reconcileTokenTypeIDs(&manifest, inputs)
	}
	if err := validateGraph(modelPath, manifest, len(labels)); err != nil {
		return nil, err
	}

	inputNames := []string{manifest.Inputs.InputIDs, manifest.Inputs.AttentionMask}
	if manifest.Inputs.TokenTypeIDs != "" {
		inputNames = append(inputNames, manifest.Inputs.TokenTypeIDs)
	}
	var session *ort.DynamicAdvancedSession
	activeProvider := ""
	var lastErr error
	for _, provider := range r.providerCandidates() {
		sessionOptions, optionsErr := ort.NewSessionOptions()
		if optionsErr != nil {
			lastErr = optionsErr
			if r.provider != "auto" {
				return nil, optionsErr
			}
			continue
		}
		if optionsErr = sessionOptions.SetGraphOptimizationLevel(ort.GraphOptimizationLevelEnableAll); optionsErr != nil {
			sessionOptions.Destroy()
			lastErr = optionsErr
			if r.provider != "auto" {
				return nil, optionsErr
			}
			continue
		}
		if provider != "cpu" {
			if optionsErr = appendExecutionProvider(sessionOptions, provider, r.deviceID); optionsErr != nil {
				sessionOptions.Destroy()
				lastErr = fmt.Errorf("enable ONNX execution provider %q: %w", provider, optionsErr)
				if r.provider != "auto" {
					return nil, lastErr
				}
				continue
			}
		}
		session, optionsErr = ort.NewDynamicAdvancedSession(modelPath, inputNames, []string{manifest.Outputs.Logits}, sessionOptions)
		sessionOptions.Destroy()
		if optionsErr == nil {
			activeProvider = provider
			break
		}
		lastErr = fmt.Errorf("create ONNX session with %s: %w", provider, optionsErr)
		if r.provider != "auto" {
			return nil, lastErr
		}
	}
	if session == nil {
		if lastErr == nil {
			lastErr = errors.New("no ONNX execution provider could create a session")
		}
		return nil, lastErr
	}
	r.providerMu.Lock()
	r.activeProvider = activeProvider
	r.providerMu.Unlock()
	return &onnxSession{
		metadata: Metadata{ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Runtime: r.Name() + "-" + activeProvider, Quantization: manifest.Quantization},
		manifest: manifest, tokenizer: tokenizer, labels: labels, session: session,
	}, nil
}

func normalizeProvider(value string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(value))
	if provider == "" {
		provider = "auto"
	}
	aliases := map[string]string{
		"dml":   "directml",
		"apple": "coreml",
		"trt":   "tensorrt",
		"rocm":  "rocm",
	}
	if alias, ok := aliases[provider]; ok {
		provider = alias
	}
	supported := map[string]bool{
		"auto": true, "cpu": true, "gpu": true, "cuda": true, "coreml": true,
		"directml": true, "tensorrt": true, "rocm": true, "openvino": true,
	}
	if !supported[provider] {
		return "", fmt.Errorf("unsupported ONNX execution provider %q (use auto, cpu, cuda, coreml, directml, tensorrt, rocm, or openvino)", value)
	}
	return provider, nil
}

func (r *onnxRuntime) providerCandidates() []string {
	if r.provider != "auto" && r.provider != "gpu" {
		return []string{r.provider}
	}
	gpuOnly := r.provider == "gpu"
	switch runtime.GOOS {
	case "darwin":
		if gpuOnly {
			return []string{"coreml"}
		}
		return []string{"coreml", "cpu"}
	case "windows":
		if gpuOnly {
			return []string{"directml", "cuda", "tensorrt"}
		}
		return []string{"directml", "cuda", "tensorrt", "cpu"}
	case "linux":
		if gpuOnly {
			return []string{"cuda", "tensorrt", "rocm", "openvino"}
		}
		return []string{"cuda", "tensorrt", "rocm", "openvino", "cpu"}
	default:
		if gpuOnly {
			return []string{}
		}
		return []string{"cpu"}
	}
}

func appendExecutionProvider(options *ort.SessionOptions, provider string, deviceID int) error {
	switch provider {
	case "coreml":
		cacheDirectory := os.Getenv("REMASK_ONNX_COREML_CACHE_DIR")
		if cacheDirectory == "" {
			cacheDirectory = filepath.Join(os.TempDir(), "remask-coreml")
		}
		if err := os.MkdirAll(cacheDirectory, 0o700); err != nil {
			return fmt.Errorf("create CoreML model cache directory: %w", err)
		}
		return options.AppendExecutionProviderCoreMLV2(map[string]string{
			"MLComputeUnits":           "ALL",
			"RequireStaticInputShapes": "0",
			"ModelCacheDirectory":      cacheDirectory,
		})
	case "directml":
		// DirectML requires sequential graph execution. Disabling the memory
		// pattern also avoids reusing CPU-side buffers across dynamic shapes.
		if err := options.SetExecutionMode(ort.ExecutionModeSequential); err != nil {
			return err
		}
		if err := options.SetMemPattern(false); err != nil {
			return err
		}
		return options.AppendExecutionProviderDirectML(deviceID)
	case "cuda":
		cudaOptions, err := ort.NewCUDAProviderOptions()
		if err != nil {
			return err
		}
		defer cudaOptions.Destroy()
		if err := cudaOptions.Update(map[string]string{"device_id": strconv.Itoa(deviceID)}); err != nil {
			return err
		}
		return options.AppendExecutionProviderCUDA(cudaOptions)
	case "tensorrt":
		tensorRTOptions, err := ort.NewTensorRTProviderOptions()
		if err != nil {
			return err
		}
		defer tensorRTOptions.Destroy()
		if err := tensorRTOptions.Update(map[string]string{"device_id": strconv.Itoa(deviceID)}); err != nil {
			return err
		}
		return options.AppendExecutionProviderTensorRT(tensorRTOptions)
	case "rocm":
		return options.AppendExecutionProvider("ROCM", map[string]string{"device_id": strconv.Itoa(deviceID)})
	case "openvino":
		return options.AppendExecutionProviderOpenVINO(map[string]string{"device_type": "GPU"})
	default:
		return fmt.Errorf("unsupported ONNX execution provider %q", provider)
	}
}

func (s *onnxSession) ID() string         { return "model:" + s.metadata.ID }
func (s *onnxSession) Metadata() Metadata { return s.metadata }

func (s *onnxSession) Detect(ctx context.Context, text string) ([]pii.Entity, error) {
	if text == "" {
		return []pii.Entity{}, nil
	}
	windows, err := s.tokenizer.encode(text, s.manifest.MaxTokens, s.manifest.Stride)
	if err != nil {
		return nil, err
	}
	predictions := make([][]tokenPrediction, 0, len(windows))
	for _, window := range windows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		logits, err := s.run(window)
		if err != nil {
			return nil, err
		}
		windowPredictions, err := predictionsFromLogits(window, logits, s.labels, s.manifest.Decoder.Type)
		if err != nil {
			return nil, err
		}
		predictions = append(predictions, windowPredictions)
	}
	return decodeEntities(text, s.ID(), mergeWindowPredictions(predictions...), s.manifest.EntityTypes, s.manifest.MinimumConfidence), nil
}

func (s *onnxSession) run(window tokenWindow) ([]float32, error) {
	shape := ort.NewShape(1, int64(len(window.InputIDs)))
	inputIDs, err := ort.NewTensor(shape, window.InputIDs)
	if err != nil {
		return nil, err
	}
	defer inputIDs.Destroy()
	attentionMask, err := ort.NewTensor(shape, window.AttentionMask)
	if err != nil {
		return nil, err
	}
	defer attentionMask.Destroy()
	inputs := []ort.Value{inputIDs, attentionMask}
	if s.manifest.Inputs.TokenTypeIDs != "" {
		tokenTypeIDs, createErr := ort.NewTensor(shape, make([]int64, len(window.InputIDs)))
		if createErr != nil {
			return nil, createErr
		}
		defer tokenTypeIDs.Destroy()
		inputs = append(inputs, tokenTypeIDs)
	}

	outputs := []ort.Value{nil}
	s.runMu.Lock()
	err = s.session.Run(inputs, outputs)
	s.runMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("run ONNX model: %w", err)
	}
	if outputs[0] == nil {
		return nil, errors.New("ONNX model did not return logits")
	}
	defer outputs[0].Destroy()
	logits, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("logits output has unsupported tensor type %T", outputs[0])
	}
	outputShape := logits.GetShape()
	if len(outputShape) != 3 || outputShape[0] != 1 || outputShape[1] != int64(len(window.InputIDs)) || outputShape[2] != int64(len(s.labels)) {
		return nil, fmt.Errorf("unexpected logits shape %s", outputShape)
	}
	return append([]float32(nil), logits.GetData()...), nil
}

func (s *onnxSession) Close() error {
	s.closeOnce.Do(func() { s.closeError = s.session.Destroy() })
	return s.closeError
}

func validateGraph(modelPath string, manifest Manifest, labelCount int) error {
	inputs, outputs, err := ort.GetInputOutputInfo(modelPath)
	if err != nil {
		return fmt.Errorf("inspect ONNX graph: %w", err)
	}
	expectedInputs := []string{manifest.Inputs.InputIDs, manifest.Inputs.AttentionMask}
	if manifest.Inputs.TokenTypeIDs != "" {
		expectedInputs = append(expectedInputs, manifest.Inputs.TokenTypeIDs)
	}
	for _, name := range expectedInputs {
		info, ok := findTensorInfo(inputs, name)
		if !ok {
			return fmt.Errorf("ONNX input %q is missing", name)
		}
		if info.DataType != ort.TensorElementDataTypeInt64 || len(info.Dimensions) != 2 {
			return fmt.Errorf("ONNX input %q must be a rank-2 int64 tensor", name)
		}
	}
	output, ok := findTensorInfo(outputs, manifest.Outputs.Logits)
	if !ok {
		return fmt.Errorf("ONNX output %q is missing", manifest.Outputs.Logits)
	}
	if output.DataType != ort.TensorElementDataTypeFloat || len(output.Dimensions) != 3 {
		return fmt.Errorf("ONNX output %q must be a rank-3 float32 tensor", manifest.Outputs.Logits)
	}
	if finalDimension := output.Dimensions[2]; finalDimension > 0 && finalDimension != int64(labelCount) {
		return fmt.Errorf("ONNX output label dimension is %d, labels contain %d entries", finalDimension, labelCount)
	}
	return nil
}

func findTensorInfo(values []ort.InputOutputInfo, name string) (ort.InputOutputInfo, bool) {
	for _, value := range values {
		if value.Name == name && value.OrtValueType == ort.ONNXTypeTensor {
			return value, true
		}
	}
	return ort.InputOutputInfo{}, false
}

func reconcileTokenTypeIDs(manifest *Manifest, inputs []ort.InputOutputInfo) {
	const inputName = "token_type_ids"

	// Preserve explicitly configured custom input names. Unlike the canonical
	// name generated by Remask, they cannot safely be assumed to be optional.
	if manifest.Inputs.TokenTypeIDs != "" && manifest.Inputs.TokenTypeIDs != inputName {
		return
	}
	if _, ok := findTensorInfo(inputs, inputName); ok {
		manifest.Inputs.TokenTypeIDs = inputName
		return
	}
	manifest.Inputs.TokenTypeIDs = ""
}

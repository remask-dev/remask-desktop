//go:build !noonnxruntime

package model

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/remask/remask-core/internal/pii"
)

type onnxRuntime struct {
	version string
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
	if libraryPath == "" {
		libraryPath = os.Getenv("REMASK_ONNXRUNTIME_LIBRARY")
	}
	if libraryPath != "" {
		ort.SetSharedLibraryPath(libraryPath)
	}
	if err := ort.InitializeEnvironment(ort.WithLogLevelWarning()); err != nil {
		return nil, fmt.Errorf("initialize ONNX Runtime: %w", err)
	}
	_ = ort.DisableTelemetry()
	return &onnxRuntime{version: ort.GetVersion()}, nil
}

func (r *onnxRuntime) Name() string    { return "onnxruntime-" + r.version }
func (r *onnxRuntime) Available() bool { return true }

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
	// Older manifests may omit optional BERT segment IDs. If the graph declares
	// the input, infer it so those packages still run correctly.
	if manifest.Inputs.TokenTypeIDs == "" {
		if inputs, _, infoErr := ort.GetInputOutputInfo(modelPath); infoErr == nil {
			if _, ok := findTensorInfo(inputs, "token_type_ids"); ok {
				manifest.Inputs.TokenTypeIDs = "token_type_ids"
			}
		}
	}
	if err := validateGraph(modelPath, manifest, len(labels)); err != nil {
		return nil, err
	}

	inputNames := []string{manifest.Inputs.InputIDs, manifest.Inputs.AttentionMask}
	if manifest.Inputs.TokenTypeIDs != "" {
		inputNames = append(inputNames, manifest.Inputs.TokenTypeIDs)
	}
	sessionOptions, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer sessionOptions.Destroy()
	if err := sessionOptions.SetGraphOptimizationLevel(ort.GraphOptimizationLevelEnableAll); err != nil {
		return nil, err
	}
	session, err := ort.NewDynamicAdvancedSession(modelPath, inputNames, []string{manifest.Outputs.Logits}, sessionOptions)
	if err != nil {
		return nil, fmt.Errorf("create ONNX session: %w", err)
	}
	return &onnxSession{
		metadata: Metadata{ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Runtime: r.Name(), Quantization: manifest.Quantization},
		manifest: manifest, tokenizer: tokenizer, labels: labels, session: session,
	}, nil
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

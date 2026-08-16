package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/remask/remask-core/internal/operation"
	"github.com/remask/remask-core/internal/pii"
)

type Manager struct {
	root          string
	readOnlyRoots []string
	runtime       Runtime
	detector      *pii.DynamicDetector
	operations    *operation.Store
	selection     *SelectionStore

	transitionMu       sync.Mutex
	mu                 sync.RWMutex
	packages           map[string]Package
	active             *managedSession
	maxInferenceTokens int
}

func (m *Manager) SetSelectionStore(store *SelectionStore) {
	m.selection = store
}

func NewManager(root string, runtime Runtime, detector *pii.DynamicDetector, operations *operation.Store) *Manager {
	if runtime == nil {
		runtime = UnavailableRuntime{}
	}
	return &Manager{root: root, runtime: runtime, detector: detector, operations: operations, packages: make(map[string]Package), maxInferenceTokens: MaxInferenceTokens}
}

// SetReadOnlyRoots adds model directories that participate in discovery but
// are never used for downloads or deletion. The writable root always wins
// when the same model ID exists in both locations.
func (m *Manager) SetReadOnlyRoots(roots ...string) {
	m.readOnlyRoots = append([]string(nil), roots...)
}

// SetMaxInferenceTokens changes the safety limit used by subsequently loaded
// model sessions. The model package's own manifest remains unchanged.
func (m *Manager) SetMaxInferenceTokens(tokens int) {
	if tokens < 1 {
		tokens = MaxInferenceTokens
	}
	if tokens > MaxInferenceTokens {
		tokens = MaxInferenceTokens
	}
	m.mu.Lock()
	m.maxInferenceTokens = tokens
	m.mu.Unlock()
}

// SetProvider changes the execution provider used by subsequently loaded
// sessions. Existing sessions keep their current provider until reloaded.
func (m *Manager) SetProvider(provider string) error {
	configurable, ok := m.runtime.(interface{ SetProvider(string) error })
	if !ok {
		return nil
	}
	return configurable.SetProvider(provider)
}

func (m *Manager) Root() string { return m.root }

func (m *Manager) Scan(ctx context.Context) ([]Package, error) {
	if err := os.MkdirAll(m.root, 0o755); err != nil {
		return nil, err
	}
	managedRoot, err := filepath.Abs(m.root)
	if err != nil {
		return nil, err
	}
	discovered := make(map[string]Package)
	seenRoots := make(map[string]bool)
	for _, root := range append(append([]string(nil), m.readOnlyRoots...), managedRoot) {
		cleanRoot, err := filepath.Abs(root)
		if err != nil {
			return nil, err
		}
		if seenRoots[cleanRoot] {
			continue
		}
		seenRoots[cleanRoot] = true
		builtIn := filepath.Clean(cleanRoot) != filepath.Clean(managedRoot)
		if err := scanRoot(ctx, cleanRoot, builtIn, discovered); err != nil {
			if builtIn && os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
	}

	m.mu.Lock()
	if m.active != nil {
		activeID := m.active.Metadata().ID
		if item, ok := discovered[activeID]; ok {
			item.Active = true
			discovered[activeID] = item
		}
	}
	m.packages = discovered
	m.mu.Unlock()
	return m.List(), nil
}

func scanRoot(ctx context.Context, root string, builtIn bool, discovered map[string]Package) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			continue
		}
		packagePath := filepath.Join(root, entry.Name())
		if _, err := os.Stat(filepath.Join(packagePath, "manifest.json")); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		item := validatePackage(packagePath)
		item.BuiltIn = builtIn
		if item.ID == "" {
			item.ID = entry.Name()
		}
		if item.ID != entry.Name() {
			item.Errors = append(item.Errors, "manifest id must match the model directory name")
			item.Valid = false
		}
		discovered[item.ID] = item
	}
	return nil
}

func (m *Manager) List() []Package {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]Package, 0, len(m.packages))
	for _, item := range m.packages {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (m *Manager) Get(id string) (Package, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	item, ok := m.packages[id]
	if !ok {
		return Package{}, ErrNotFound
	}
	return item, nil
}

func (m *Manager) Active() (Metadata, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.active == nil {
		return Metadata{}, false
	}
	return m.active.Metadata(), true
}

func (m *Manager) RuntimeStatus() map[string]any {
	status := map[string]any{"name": m.runtime.Name(), "available": m.runtime.Available()}
	m.mu.RLock()
	configuredMaxTokens := m.maxInferenceTokens
	active := m.active
	m.mu.RUnlock()
	status["max_inference_tokens"] = configuredMaxTokens
	if active != nil && active.MaxInferenceTokens() > 0 {
		status["max_inference_tokens"] = active.MaxInferenceTokens()
		if active.MaxInferenceTokens() != configuredMaxTokens {
			status["configured_max_inference_tokens"] = configuredMaxTokens
			status["inference_config_pending"] = true
		}
	}
	if provider, ok := m.runtime.(interface{ Provider() string }); ok {
		status["provider"] = provider.Provider()
	}
	if configured, ok := m.runtime.(interface{ ConfiguredProvider() string }); ok {
		status["configured_provider"] = configured.ConfiguredProvider()
		if active == nil {
			status["provider"] = configured.ConfiguredProvider()
		}
		if activeProvider, activeOK := m.runtime.(interface{ ActiveProvider() string }); activeOK && active != nil && activeProvider.ActiveProvider() != "" && activeProvider.ActiveProvider() != configured.ConfiguredProvider() {
			status["provider_config_pending"] = true
		}
	}
	return status
}

func (m *Manager) Activate(id string) (operation.Operation, error) {
	item, err := m.Get(id)
	if err != nil {
		return operation.Operation{}, err
	}
	if !item.Valid {
		return operation.Operation{}, errors.New("model package is invalid")
	}
	op, ctx := m.operations.Create("model.activate")
	go m.activate(ctx, op.ID, item)
	return op, nil
}

func (m *Manager) ActivateSync(ctx context.Context, id string) error {
	item, err := m.Get(id)
	if err != nil {
		return err
	}
	if !item.Valid {
		return errors.New("model package is invalid")
	}
	session, err := m.loadSession(ctx, item)
	if err != nil {
		return err
	}
	if err := m.commitActive(item.ID, session); err != nil {
		_ = session.Close()
		return err
	}
	return nil
}

func (m *Manager) activate(ctx context.Context, operationID string, item Package) {
	_ = m.operations.Update(operationID, func(op *operation.Operation) {
		op.Status = operation.StatusRunning
		op.Progress = 10
		op.Message = "loading model"
	})
	managed, err := m.loadSession(ctx, item)
	if err != nil {
		_ = m.operations.Fail(operationID, err)
		return
	}
	if err := m.commitActive(item.ID, managed); err != nil {
		_ = managed.Close()
		_ = m.operations.Fail(operationID, err)
		return
	}
	_ = m.operations.Complete(operationID, map[string]any{"model": managed.Metadata()})
}

func (m *Manager) loadSession(ctx context.Context, item Package) (*managedSession, error) {
	manifest := item.Manifest
	m.mu.RLock()
	maxInferenceTokens := m.maxInferenceTokens
	m.mu.RUnlock()
	if maxInferenceTokens < 1 || maxInferenceTokens > MaxInferenceTokens {
		maxInferenceTokens = MaxInferenceTokens
	}
	if manifest.MaxTokens > maxInferenceTokens {
		manifest.MaxTokens = maxInferenceTokens
		if manifest.Stride >= manifest.MaxTokens {
			manifest.Stride = maxInferenceTokens / 8
		}
	}
	session, err := m.runtime.Load(ctx, item.Path, manifest)
	if err != nil {
		return nil, err
	}
	managed := newManagedSessionWithLimit(session, manifest.MaxTokens)

	selfTestText := item.Manifest.SelfTestText
	if selfTestText == "" {
		selfTestText = "Remask model self test"
	}
	if _, err := managed.Detect(ctx, selfTestText); err != nil {
		_ = managed.Close()
		return nil, fmt.Errorf("model self test: %w", err)
	}
	return managed, nil
}

func (m *Manager) commitActive(id string, managed *managedSession) error {
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()
	if err := m.selection.Save(id); err != nil {
		return fmt.Errorf("persist active model: %w", err)
	}
	previousDetector := m.detector.Swap(managed)
	m.mu.Lock()
	previousSession := m.active
	m.active = managed
	for packageID, candidate := range m.packages {
		candidate.Active = packageID == id
		m.packages[packageID] = candidate
	}
	m.mu.Unlock()

	if previousDetector != nil && previousSession != nil {
		_ = previousSession.Close()
	}
	return nil
}

func (m *Manager) Unload() error {
	m.transitionMu.Lock()
	defer m.transitionMu.Unlock()
	if err := m.selection.Save(""); err != nil {
		return fmt.Errorf("persist unloaded model: %w", err)
	}
	previousDetector := m.detector.Swap(nil)
	m.mu.Lock()
	previous := m.active
	m.active = nil
	for id, item := range m.packages {
		item.Active = false
		m.packages[id] = item
	}
	m.mu.Unlock()
	if previousDetector != nil && previous != nil {
		return previous.Close()
	}
	return nil
}

// Delete removes a downloaded model package from the managed models directory.
// The active model cannot be deleted; callers must unload it first.
func (m *Manager) Delete(id string) error {
	m.mu.RLock()
	item, ok := m.packages[id]
	active := m.active
	m.mu.RUnlock()
	if !ok {
		return ErrNotFound
	}
	if item.BuiltIn {
		return ErrReadOnly
	}
	if active != nil && active.Metadata().ID == id {
		return errors.New("cannot delete the active model; unload it first")
	}
	target, err := secureModelPath(m.root, id)
	if err != nil {
		return err
	}
	if filepath.Clean(target) == filepath.Clean(item.Path) {
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		m.mu.Lock()
		delete(m.packages, id)
		m.mu.Unlock()
		return nil
	}
	return errors.New("model directory does not match the managed models directory")
}

func validatePackage(packagePath string) Package {
	item := Package{Path: packagePath, UpdatedAt: time.Now().UTC(), Errors: []string{}}
	manifestPath := filepath.Join(packagePath, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		item.Errors = append(item.Errors, "manifest.json: "+err.Error())
		return item
	}
	if err := json.Unmarshal(data, &item.Manifest); err != nil {
		item.Errors = append(item.Errors, "manifest.json: "+err.Error())
		return item
	}
	item.ID = item.Manifest.ID
	item.Errors = append(item.Errors, validateManifest(item.Manifest)...)
	for name, spec := range item.Manifest.Files {
		filePath, pathErr := secureModelPath(packagePath, spec.Path)
		if pathErr != nil {
			item.Errors = append(item.Errors, name+": "+pathErr.Error())
			continue
		}
		info, statErr := os.Stat(filePath)
		if statErr != nil {
			item.Errors = append(item.Errors, name+": "+statErr.Error())
			continue
		}
		if !info.Mode().IsRegular() {
			item.Errors = append(item.Errors, name+": not a regular file")
			continue
		}
		if spec.Size > 0 && info.Size() != spec.Size {
			item.Errors = append(item.Errors, fmt.Sprintf("%s: size mismatch", name))
		}
		if spec.SHA256 != "" {
			digest, digestErr := fileSHA256(filePath)
			if digestErr != nil || !strings.EqualFold(digest, spec.SHA256) {
				item.Errors = append(item.Errors, name+": sha256 mismatch")
			}
		}
	}
	item.Valid = len(item.Errors) == 0
	return item
}

func validateManifest(manifest Manifest) []string {
	var result []string
	if manifest.SchemaVersion != 1 {
		result = append(result, "schema_version must be 1")
	}
	if manifest.ID == "" || strings.ContainsAny(manifest.ID, "/\\") {
		result = append(result, "id is invalid")
	}
	if manifest.Task != "token-classification" {
		result = append(result, "task must be token-classification")
	}
	if manifest.LabelScheme != "BIO" && manifest.LabelScheme != "BIOES" {
		result = append(result, "label_scheme must be BIO or BIOES")
	}
	minimumTokens := 3
	strideLimit := manifest.MaxTokens - 2
	if manifest.Tokenizer.Type == "o200k-base" {
		minimumTokens = 1
		strideLimit = manifest.MaxTokens
	}
	if manifest.MaxTokens < minimumTokens {
		result = append(result, fmt.Sprintf("max_tokens must be at least %d", minimumTokens))
	}
	if manifest.Stride < 0 || manifest.Stride >= strideLimit {
		result = append(result, "stride is invalid for the configured tokenizer")
	}
	for _, required := range []string{"model", "tokenizer", "labels"} {
		if _, ok := manifest.Files[required]; !ok {
			result = append(result, "files."+required+" is required")
		}
	}
	if manifest.Inputs.InputIDs == "" || manifest.Inputs.AttentionMask == "" || manifest.Outputs.Logits == "" {
		result = append(result, "input and output tensor names are required")
	}
	if manifest.Tokenizer.Type != "" && manifest.Tokenizer.Type != "bert-wordpiece" && manifest.Tokenizer.Type != "o200k-base" {
		result = append(result, "tokenizer_config.type is unsupported")
	}
	if manifest.Decoder.Type != "" && manifest.Decoder.Type != "argmax" && manifest.Decoder.Type != "viterbi-bioes" {
		result = append(result, "decoder.type is unsupported")
	}
	if manifest.Decoder.Calibration != "" {
		if _, ok := manifest.Files["calibration"]; !ok {
			result = append(result, "files.calibration is required by decoder.calibration_file")
		}
	}
	for label, confidence := range manifest.MinimumConfidence {
		if confidence < 0 || confidence > 1 {
			result = append(result, "minimum_confidence."+label+" must be between 0 and 1")
		}
	}
	return result
}

func secureModelPath(root, relative string) (string, error) {
	if filepath.IsAbs(relative) || relative == "" {
		return "", errors.New("file path must be relative")
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(cleanRoot, relative))
	if err != nil {
		return "", err
	}
	if path != cleanRoot && !strings.HasPrefix(path, cleanRoot+string(os.PathSeparator)) {
		return "", errors.New("file path escapes model directory")
	}
	return path, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

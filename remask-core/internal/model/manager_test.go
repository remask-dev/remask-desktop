package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/remask/remask-core/internal/operation"
	"github.com/remask/remask-core/internal/pii"
)

func TestScanValidatesModelPackage(t *testing.T) {
	root := t.TempDir()
	createTestPackage(t, root, "privacy-q4")
	manager := NewManager(root, UnavailableRuntime{}, pii.NewDynamicDetector(pii.NewRuleDetector()), operation.NewStore())
	packages, err := manager.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 1 || !packages[0].Valid || packages[0].ID != "privacy-q4" {
		t.Fatalf("unexpected packages: %#v", packages)
	}
}

func TestScanRejectsManifestWithoutSequenceSettings(t *testing.T) {
	root := t.TempDir()
	createTestPackage(t, root, "incomplete-model")
	manifestPath := filepath.Join(root, "incomplete-model", "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	delete(manifest, "max_tokens")
	delete(manifest, "stride")
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	item := validatePackage(filepath.Dir(manifestPath))
	if item.Valid || item.Manifest.MaxTokens != 0 || item.Manifest.Stride != 0 {
		t.Fatalf("incomplete manifest was accepted or filled: %#v", item)
	}
}

func TestScanIncludesReadOnlyAndManagedModels(t *testing.T) {
	managedRoot := t.TempDir()
	builtinRoot := t.TempDir()
	createTestPackage(t, builtinRoot, "builtin-model")
	createTestPackage(t, managedRoot, "user-model")
	manager := NewManager(managedRoot, UnavailableRuntime{}, pii.NewDynamicDetector(pii.NewRuleDetector()), operation.NewStore())
	manager.SetReadOnlyRoots(builtinRoot)
	packages, err := manager.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 2 || packages[0].ID != "builtin-model" || !packages[0].BuiltIn || packages[1].ID != "user-model" || packages[1].BuiltIn {
		t.Fatalf("unexpected packages: %#v", packages)
	}
}

func TestManagedModelOverridesReadOnlyModelWithSameID(t *testing.T) {
	managedRoot := t.TempDir()
	builtinRoot := t.TempDir()
	createTestPackage(t, builtinRoot, "shared-model")
	createTestPackage(t, managedRoot, "shared-model")
	manager := NewManager(managedRoot, UnavailableRuntime{}, pii.NewDynamicDetector(pii.NewRuleDetector()), operation.NewStore())
	manager.SetReadOnlyRoots(builtinRoot)
	packages, err := manager.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 1 || packages[0].BuiltIn || packages[0].Path != filepath.Join(managedRoot, "shared-model") {
		t.Fatalf("managed package must win: %#v", packages)
	}
}

func TestDeleteRejectsReadOnlyModel(t *testing.T) {
	managedRoot := t.TempDir()
	builtinRoot := t.TempDir()
	createTestPackage(t, builtinRoot, "builtin-model")
	manager := NewManager(managedRoot, UnavailableRuntime{}, pii.NewDynamicDetector(pii.NewRuleDetector()), operation.NewStore())
	manager.SetReadOnlyRoots(builtinRoot)
	if _, err := manager.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Delete("builtin-model"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("delete error = %v, want ErrReadOnly", err)
	}
	if _, err := os.Stat(filepath.Join(builtinRoot, "builtin-model", "manifest.json")); err != nil {
		t.Fatalf("built-in model was modified: %v", err)
	}
}

func TestScanIgnoresNestedModelPackages(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "publisher", "model", "q4")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	createTestPackage(t, filepath.Dir(nested), filepath.Base(nested))
	manager := NewManager(root, UnavailableRuntime{}, pii.NewDynamicDetector(pii.NewRuleDetector()), operation.NewStore())
	packages, err := manager.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 0 {
		t.Fatalf("nested packages must be ignored: %#v", packages)
	}
}

func TestScanRejectsDirectoryAndManifestIDMismatch(t *testing.T) {
	root := t.TempDir()
	createTestPackage(t, root, "manifest-id")
	if err := os.Rename(filepath.Join(root, "manifest-id"), filepath.Join(root, "directory-id")); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(root, UnavailableRuntime{}, pii.NewDynamicDetector(pii.NewRuleDetector()), operation.NewStore())
	packages, err := manager.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(packages) != 1 || packages[0].Valid {
		t.Fatalf("mismatched package must be invalid: %#v", packages)
	}
}

func TestActivationFailsCleanlyWhenRuntimeUnavailable(t *testing.T) {
	root := t.TempDir()
	createTestPackage(t, root, "privacy-q4")
	operations := operation.NewStore()
	manager := NewManager(root, UnavailableRuntime{}, pii.NewDynamicDetector(pii.NewRuleDetector()), operations)
	_, _ = manager.Scan(context.Background())
	op, err := manager.Activate("privacy-q4")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		current, getErr := operations.Get(op.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if current.Status == operation.StatusFailed {
			if _, active := manager.Active(); active {
				t.Fatal("failed activation must not replace active model")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("operation did not finish")
}

func TestActivateSyncLoadsAndMarksModelActive(t *testing.T) {
	root := t.TempDir()
	createTestPackage(t, root, "privacy-q4")
	detector := pii.NewDynamicDetector(pii.NewRuleDetector())
	manager := NewManager(root, testRuntime{}, detector, operation.NewStore())
	if _, err := manager.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.ActivateSync(context.Background(), "privacy-q4"); err != nil {
		t.Fatal(err)
	}
	active, ok := manager.Active()
	if !ok || active.ID != "privacy-q4" {
		t.Fatalf("unexpected active model: %#v, active=%v", active, ok)
	}
	packages := manager.List()
	if len(packages) != 1 || !packages[0].Active {
		t.Fatalf("package was not marked active: %#v", packages)
	}
}

func TestModelChangeHookRunsAfterActivationAndUnload(t *testing.T) {
	root := t.TempDir()
	createTestPackage(t, root, "privacy-q4")
	manager := NewManager(root, testRuntime{}, pii.NewDynamicDetector(pii.NewRuleDetector()), operation.NewStore())
	changes := 0
	manager.SetModelChangeHook(func() { changes++ })
	if _, err := manager.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.ActivateSync(context.Background(), "privacy-q4"); err != nil {
		t.Fatal(err)
	}
	if changes != 1 {
		t.Fatalf("model change hook ran %d times after activation, want 1", changes)
	}
	if err := manager.Unload(); err != nil {
		t.Fatal(err)
	}
	if changes != 2 {
		t.Fatalf("model change hook ran %d times after unload, want 2", changes)
	}
}

func TestModelSelectionPersistsActivationAndExplicitUnload(t *testing.T) {
	root := t.TempDir()
	dataDir := t.TempDir()
	createTestPackage(t, root, "privacy-q4")
	manager := NewManager(root, testRuntime{}, pii.NewDynamicDetector(pii.NewRuleDetector()), operation.NewStore())
	selection := NewSelectionStore(dataDir)
	manager.SetSelectionStore(selection)
	if _, err := manager.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.ActivateSync(context.Background(), "privacy-q4"); err != nil {
		t.Fatal(err)
	}
	if selected, configured, err := selection.Load(); err != nil || !configured || selected != "privacy-q4" {
		t.Fatalf("selection after activation = %q, configured=%v, err=%v", selected, configured, err)
	}
	if err := manager.Unload(); err != nil {
		t.Fatal(err)
	}
	if selected, configured, err := selection.Load(); err != nil || !configured || selected != "" {
		t.Fatalf("selection after unload = %q, configured=%v, err=%v", selected, configured, err)
	}
}

type testRuntime struct{}

func (testRuntime) Name() string    { return "test" }
func (testRuntime) Available() bool { return true }
func (testRuntime) Load(_ context.Context, _ string, manifest Manifest) (Session, error) {
	return &testSession{metadata: Metadata{ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Runtime: "test"}}, nil
}

type testSession struct {
	metadata Metadata
}

func (s *testSession) ID() string { return "model:" + s.metadata.ID }
func (s *testSession) Detect(_ context.Context, _ string) ([]pii.Entity, error) {
	return []pii.Entity{}, nil
}
func (s *testSession) Metadata() Metadata { return s.metadata }
func (s *testSession) Close() error       { return nil }

func createTestPackage(t *testing.T, root, id string) {
	t.Helper()
	directory := filepath.Join(root, id)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		"model.onnx":     []byte("test-model"),
		"tokenizer.json": []byte(`{"version":"1.0"}`),
		"labels.json":    []byte(`{"0":"O"}`),
	}
	manifestFiles := map[string]FileSpec{}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(directory, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		key := map[string]string{"model.onnx": "model", "tokenizer.json": "tokenizer", "labels.json": "labels"}[name]
		manifestFiles[key] = FileSpec{Path: name, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(data))}
	}
	manifest := Manifest{
		SchemaVersion: 1, ID: id, Name: "Test model", Version: "1.0.0",
		Task: "token-classification", Quantization: "int4", LabelScheme: "BIO",
		MaxTokens: 512, Stride: 64, Files: manifestFiles,
		Inputs:  InputSpec{InputIDs: "input_ids", AttentionMask: "attention_mask"},
		Outputs: OutputSpec{Logits: "logits"},
	}
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(directory, "manifest.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

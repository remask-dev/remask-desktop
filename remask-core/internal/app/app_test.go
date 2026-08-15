package app

import (
	"testing"

	"github.com/remask/remask-core/internal/model"
)

func TestStartupModelUsesSavedSelection(t *testing.T) {
	selection := model.NewSelectionStore(t.TempDir())
	if err := selection.Save("user-model"); err != nil {
		t.Fatal(err)
	}
	packages := []model.Package{{ID: "first-model", Valid: true}, {ID: "user-model", Valid: true}}
	selected, err := startupModelID("", selection, packages)
	if err != nil {
		t.Fatal(err)
	}
	if selected != "user-model" {
		t.Fatalf("startup model = %q, want saved selection", selected)
	}
}

func TestStartupModelUsesFirstValidModelOnlyWithoutSelection(t *testing.T) {
	selection := model.NewSelectionStore(t.TempDir())
	packages := []model.Package{{ID: "invalid-model"}, {ID: "first-valid", Valid: true}, {ID: "second-valid", Valid: true}}
	selected, err := startupModelID("", selection, packages)
	if err != nil {
		t.Fatal(err)
	}
	if selected != "first-valid" {
		t.Fatalf("startup model = %q, want first valid model", selected)
	}
}

func TestStartupModelHonorsExplicitUnload(t *testing.T) {
	selection := model.NewSelectionStore(t.TempDir())
	if err := selection.Save(""); err != nil {
		t.Fatal(err)
	}
	selected, err := startupModelID("", selection, []model.Package{{ID: "first-model", Valid: true}})
	if err != nil {
		t.Fatal(err)
	}
	if selected != "" {
		t.Fatalf("startup model = %q, want no model after explicit unload", selected)
	}
}

func TestStartupModelHonorsConfiguredOverride(t *testing.T) {
	selection := model.NewSelectionStore(t.TempDir())
	if err := selection.Save("saved-model"); err != nil {
		t.Fatal(err)
	}
	selected, err := startupModelID("configured-model", selection, nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected != "configured-model" {
		t.Fatalf("startup model = %q, want configured override", selected)
	}
}

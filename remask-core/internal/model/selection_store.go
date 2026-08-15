package model

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const selectionFilename = "model-selection.json"

type savedSelection struct {
	ActiveModel string `json:"active_model"`
}

// SelectionStore distinguishes a missing preference from an explicitly
// unloaded model. A missing file means the Core may choose an initial model;
// an existing file with an empty active_model means start with rules only.
type SelectionStore struct {
	mu   sync.Mutex
	path string
}

func NewSelectionStore(dataDir string) *SelectionStore {
	if strings.TrimSpace(dataDir) == "" {
		return &SelectionStore{}
	}
	return &SelectionStore{path: filepath.Join(dataDir, selectionFilename)}
}

func (s *SelectionStore) Load() (id string, configured bool, err error) {
	if s == nil || s.path == "" {
		return "", false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var selection savedSelection
	if err := json.Unmarshal(data, &selection); err != nil {
		return "", false, err
	}
	return selection.ActiveModel, true, nil
}

func (s *SelectionStore) Save(id string) error {
	if s == nil || s.path == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(savedSelection{ActiveModel: id}, "", "  ")
	if err != nil {
		return err
	}
	temporary := s.path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, s.path)
}

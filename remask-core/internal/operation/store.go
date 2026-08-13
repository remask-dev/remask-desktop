package operation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrNotFound = errors.New("operation not found")

type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

type Operation struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Status     Status         `json:"status"`
	Progress   int            `json:"progress"`
	Message    string         `json:"message,omitempty"`
	Error      string         `json:"error,omitempty"`
	Result     map[string]any `json:"result,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	FinishedAt *time.Time     `json:"finished_at,omitempty"`
}

type entry struct {
	operation Operation
	cancel    context.CancelFunc
}

type Store struct {
	mu    sync.RWMutex
	items map[string]*entry
}

func NewStore() *Store { return &Store{items: make(map[string]*entry)} }

func (s *Store) Create(operationType string) (Operation, context.Context) {
	id := "op_" + randomID()
	now := time.Now().UTC()
	ctx, cancel := context.WithCancel(context.Background())
	item := &entry{operation: Operation{
		ID: id, Type: operationType, Status: StatusPending, Progress: 0,
		CreatedAt: now, UpdatedAt: now,
	}, cancel: cancel}
	s.mu.Lock()
	s.items[id] = item
	s.mu.Unlock()
	return item.operation, ctx
}

func (s *Store) Get(id string) (Operation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[id]
	if !ok {
		return Operation{}, ErrNotFound
	}
	return clone(item.operation), nil
}

func (s *Store) List() []Operation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Operation, 0, len(s.items))
	for _, item := range s.items {
		result = append(result, clone(item.operation))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result
}

func (s *Store) Update(id string, update func(*Operation)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return ErrNotFound
	}
	update(&item.operation)
	item.operation.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *Store) Complete(id string, result map[string]any) error {
	return s.finish(id, StatusSucceeded, "", result)
}

func (s *Store) Fail(id string, err error) error {
	message := "operation failed"
	if err != nil {
		message = err.Error()
	}
	return s.finish(id, StatusFailed, message, nil)
}

func (s *Store) Cancel(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.items[id]
	if !ok {
		return ErrNotFound
	}
	item.cancel()
	now := time.Now().UTC()
	item.operation.Status = StatusCancelled
	item.operation.Progress = 100
	item.operation.UpdatedAt = now
	item.operation.FinishedAt = &now
	return nil
}

func (s *Store) finish(id string, status Status, errorMessage string, result map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.items[id]
	if !ok {
		return ErrNotFound
	}
	if entry.operation.Status == StatusCancelled || entry.operation.Status == StatusSucceeded || entry.operation.Status == StatusFailed {
		return nil
	}
	item := &entry.operation
	now := time.Now().UTC()
	item.Status = status
	item.Progress = 100
	item.Error = errorMessage
	item.Result = result
	item.UpdatedAt = now
	item.FinishedAt = &now
	return nil
}

func clone(item Operation) Operation {
	if item.Result != nil {
		original := item.Result
		item.Result = make(map[string]any, len(original))
		for key, value := range original {
			item.Result[key] = value
		}
	}
	return item
}

func randomID() string {
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return strings.Repeat("0", 24)
	}
	return hex.EncodeToString(buffer)
}

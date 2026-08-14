package model

import (
	"context"
	"sync"

	"github.com/remask/remask-core/internal/pii"
)

type managedSession struct {
	session   Session
	maxTokens int
	mu        sync.Mutex
	cond      *sync.Cond
	active    int
	closing   bool
	closed    bool
}

func newManagedSession(session Session) *managedSession {
	return newManagedSessionWithLimit(session, 0)
}

func newManagedSessionWithLimit(session Session, maxTokens int) *managedSession {
	managed := &managedSession{session: session}
	managed.maxTokens = maxTokens
	managed.cond = sync.NewCond(&managed.mu)
	return managed
}

func (s *managedSession) MaxInferenceTokens() int { return s.maxTokens }

func (s *managedSession) ID() string { return s.session.ID() }

func (s *managedSession) Detect(ctx context.Context, text string) ([]pii.Entity, error) {
	s.mu.Lock()
	if s.closing || s.closed {
		s.mu.Unlock()
		return nil, context.Canceled
	}
	s.active++
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.active--
		if s.active == 0 {
			s.cond.Broadcast()
		}
		s.mu.Unlock()
	}()
	return s.session.Detect(ctx, text)
}

func (s *managedSession) Metadata() Metadata { return s.session.Metadata() }

func (s *managedSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closing = true
	for s.active > 0 {
		s.cond.Wait()
	}
	s.closed = true
	s.mu.Unlock()
	return s.session.Close()
}

package memory

import (
	"context"
	"sync"

	"github.com/kumori-sh/spacetrk/pkg/exception"
	"github.com/kumori-sh/spacetrk/src/core/domain/session"
)

// SessionRepository is a thread-safe, in-memory implementation of session.Repository.
type SessionRepository struct {
	mu       sync.RWMutex
	sessions map[string]*session.Session
}

func NewSessionRepository() *SessionRepository {
	return &SessionRepository{sessions: make(map[string]*session.Session)}
}

func (r *SessionRepository) Create(_ context.Context, s *session.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *s
	cp.Messages = make([]session.Message, len(s.Messages))
	copy(cp.Messages, s.Messages)
	r.sessions[s.ID] = &cp
	return nil
}

func (r *SessionRepository) GetByID(_ context.Context, id string) (*session.Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[id]
	if !ok {
		return nil, exception.NotFound("session", id)
	}
	cp := *s
	cp.Messages = make([]session.Message, len(s.Messages))
	copy(cp.Messages, s.Messages)
	return &cp, nil
}

func (r *SessionRepository) Update(_ context.Context, s *session.Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[s.ID]; !ok {
		return exception.NotFound("session", s.ID)
	}
	cp := *s
	cp.Messages = make([]session.Message, len(s.Messages))
	copy(cp.Messages, s.Messages)
	r.sessions[s.ID] = &cp
	return nil
}

func (r *SessionRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[id]; !ok {
		return exception.NotFound("session", id)
	}
	delete(r.sessions, id)
	return nil
}

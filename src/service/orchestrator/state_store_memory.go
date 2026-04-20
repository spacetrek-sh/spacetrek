package orchestratorsvc

import (
	"context"
	"sync"
	"time"

	orchdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/orchestrator"
)

// MemoryStateStore keeps orchestrator state in process memory.
type MemoryStateStore struct {
	mu     sync.RWMutex
	states map[string]orchdomain.State
}

func NewMemoryStateStore() *MemoryStateStore {
	return &MemoryStateStore{states: make(map[string]orchdomain.State)}
}

func (s *MemoryStateStore) Load(_ context.Context, chatID string) (orchdomain.State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if st, ok := s.states[chatID]; ok {
		return st, nil
	}
	return orchdomain.State{ChatID: chatID, UpdatedAt: time.Now().UTC()}, nil
}

func (s *MemoryStateStore) Save(_ context.Context, state orchdomain.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[state.ChatID] = state
	return nil
}

package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/agent"
)

// AgentMemoryRepository is a thread-safe in-memory implementation of
// agent.MemoryRepository. Intended for development and tests; expired
// entries are filtered on read rather than reaped.
type AgentMemoryRepository struct {
	mu      sync.RWMutex
	entries map[string]*agent.MemoryEntry
}

func NewAgentMemoryRepository() *AgentMemoryRepository {
	return &AgentMemoryRepository{entries: make(map[string]*agent.MemoryEntry)}
}

func agentMemoryKey(chatID, key string) string {
	return chatID + "\x00" + key
}

func (r *AgentMemoryRepository) Set(_ context.Context, entry *agent.MemoryEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UTC()
	cp := *entry
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = now
	}
	cp.UpdatedAt = now
	r.entries[agentMemoryKey(entry.ChatID, entry.Key)] = &cp
	return nil
}

func (r *AgentMemoryRepository) Get(_ context.Context, chatID, key string) (*agent.MemoryEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.entries[agentMemoryKey(chatID, key)]
	if !ok || isAgentMemoryExpired(entry) {
		return nil, exception.NotFound("agent_memory", key)
	}
	cp := *entry
	return &cp, nil
}

func (r *AgentMemoryRepository) Delete(_ context.Context, chatID, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	k := agentMemoryKey(chatID, key)
	entry, ok := r.entries[k]
	if !ok || isAgentMemoryExpired(entry) {
		return exception.NotFound("agent_memory", key)
	}
	delete(r.entries, k)
	return nil
}

func (r *AgentMemoryRepository) List(_ context.Context, chatID string) ([]*agent.MemoryEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*agent.MemoryEntry, 0)
	for _, entry := range r.entries {
		if entry.ChatID != chatID {
			continue
		}
		if isAgentMemoryExpired(entry) {
			continue
		}
		cp := *entry
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func isAgentMemoryExpired(entry *agent.MemoryEntry) bool {
	return !entry.ExpiresAt.IsZero() && time.Now().After(entry.ExpiresAt)
}

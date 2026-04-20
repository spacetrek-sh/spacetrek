package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/agent"
)

// AgentRepository is a thread-safe, in-memory implementation of agent.Repository.
// Intended for development and testing; not for production use.
type AgentRepository struct {
	mu     sync.RWMutex
	agents map[string]*agent.Agent
}

func NewAgentRepository() *AgentRepository {
	return &AgentRepository{agents: make(map[string]*agent.Agent)}
}

func (r *AgentRepository) Create(_ context.Context, a *agent.Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *a
	r.agents[a.ID] = &cp
	return nil
}

func (r *AgentRepository) GetByID(_ context.Context, id string) (*agent.Agent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.agents[id]
	if !ok {
		return nil, exception.NotFound("agent", id)
	}
	cp := *a
	return &cp, nil
}

func (r *AgentRepository) List(_ context.Context, offset, limit int) ([]*agent.Agent, int64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*agent.Agent, 0, len(r.agents))
	for _, a := range r.agents {
		cp := *a
		all = append(all, &cp)
	}
	// Deterministic ordering: newest first
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})

	total := int64(len(all))
	if offset >= len(all) {
		return []*agent.Agent{}, total, nil
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return all[offset:end], total, nil
}

func (r *AgentRepository) Update(_ context.Context, a *agent.Agent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.agents[a.ID]; !ok {
		return exception.NotFound("agent", a.ID)
	}
	cp := *a
	r.agents[a.ID] = &cp
	return nil
}

func (r *AgentRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.agents[id]; !ok {
		return exception.NotFound("agent", id)
	}
	delete(r.agents, id)
	return nil
}

package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/kumori-sh/spacetrk/pkg/exception"
	"github.com/kumori-sh/spacetrk/src/core/domain/environment"
)

// EnvironmentRepository is a thread-safe in-memory environment repository.
type EnvironmentRepository struct {
	mu           sync.RWMutex
	environments map[string]*environment.Environment
}

func NewEnvironmentRepository() *EnvironmentRepository {
	return &EnvironmentRepository{environments: make(map[string]*environment.Environment)}
}

func (r *EnvironmentRepository) Create(_ context.Context, env *environment.Environment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *env
	r.environments[env.ID] = &cp
	return nil
}

func (r *EnvironmentRepository) GetByID(_ context.Context, id string) (*environment.Environment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	env, ok := r.environments[id]
	if !ok {
		return nil, exception.NotFound("environment", id)
	}
	cp := *env
	return &cp, nil
}

func (r *EnvironmentRepository) List(_ context.Context) ([]*environment.Environment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*environment.Environment, 0, len(r.environments))
	for _, env := range r.environments {
		cp := *env
		out = append(out, &cp)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})

	return out, nil
}

func (r *EnvironmentRepository) Update(_ context.Context, env *environment.Environment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.environments[env.ID]; !ok {
		return exception.NotFound("environment", env.ID)
	}
	cp := *env
	r.environments[env.ID] = &cp
	return nil
}

func (r *EnvironmentRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.environments[id]; !ok {
		return exception.NotFound("environment", id)
	}
	delete(r.environments, id)
	return nil
}

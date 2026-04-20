package orchestratorsvc

import (
	"sync"

	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
)

// InMemoryToolRegistry is the bootstrap tool registry implementation.
type InMemoryToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]tool.Tool
}

func NewInMemoryToolRegistry(initial []tool.Tool) *InMemoryToolRegistry {
	reg := &InMemoryToolRegistry{tools: make(map[string]tool.Tool)}
	for _, t := range initial {
		if t == nil {
			continue
		}
		reg.Register(t)
	}
	return reg
}

func (r *InMemoryToolRegistry) Register(t tool.Tool) {
	if t == nil {
		return
	}
	def := t.Definition()
	if def.Name == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[def.Name] = t
}

func (r *InMemoryToolRegistry) Get(name string) (tool.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

func (r *InMemoryToolRegistry) List() []tool.Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]tool.Definition, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Definition())
	}
	return out
}

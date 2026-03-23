package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/kumori-sh/spacetrk/pkg/exception"
	vmdomain "github.com/kumori-sh/spacetrk/src/core/domain/vm"
)

// VMRepository is a thread-safe in-memory VM repository.
type VMRepository struct {
	mu  sync.RWMutex
	vms map[string]*vmdomain.VM
}

func NewVMRepository() *VMRepository {
	return &VMRepository{vms: make(map[string]*vmdomain.VM)}
}

func (r *VMRepository) Create(_ context.Context, vm *vmdomain.VM) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *vm
	r.vms[vm.ID] = &cp
	return nil
}

func (r *VMRepository) GetByID(_ context.Context, id string) (*vmdomain.VM, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	vm, ok := r.vms[id]
	if !ok {
		return nil, exception.NotFound("vm", id)
	}
	cp := *vm
	return &cp, nil
}

func (r *VMRepository) Update(_ context.Context, vm *vmdomain.VM) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.vms[vm.ID]; !ok {
		return exception.NotFound("vm", vm.ID)
	}
	cp := *vm
	r.vms[vm.ID] = &cp
	return nil
}

func (r *VMRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.vms[id]; !ok {
		return exception.NotFound("vm", id)
	}
	delete(r.vms, id)
	return nil
}

func (r *VMRepository) List(_ context.Context) ([]*vmdomain.VM, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*vmdomain.VM, 0, len(r.vms))
	for _, vm := range r.vms {
		cp := *vm
		out = append(out, &cp)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})

	return out, nil
}

func (r *VMRepository) GetAvailablePool(_ context.Context, provider vmdomain.Provider, limit int) ([]*vmdomain.VM, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*vmdomain.VM, 0, limit)
	for _, vm := range r.vms {
		if vm.Provider != provider {
			continue
		}
		if !vm.IsAvailable() {
			continue
		}
		cp := *vm
		out = append(out, &cp)
		if limit > 0 && len(out) >= limit {
			break
		}
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})

	return out, nil
}

func (r *VMRepository) GetByEnvironmentID(_ context.Context, envID string) ([]*vmdomain.VM, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*vmdomain.VM, 0)
	for _, vm := range r.vms {
		if vm.EnvironmentID != envID {
			continue
		}
		cp := *vm
		out = append(out, &cp)
	}

	return out, nil
}

func (r *VMRepository) GetByChatID(_ context.Context, chatID string) (*vmdomain.VM, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, vm := range r.vms {
		if vm.ChatID != nil && *vm.ChatID == chatID {
			cp := *vm
			return &cp, nil
		}
	}

	return nil, exception.NotFound("vm chat", chatID)
}

func (r *VMRepository) GetActiveVMs(_ context.Context) ([]*vmdomain.VM, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]*vmdomain.VM, 0)
	for _, vm := range r.vms {
		if !vm.Status.IsActive() {
			continue
		}
		cp := *vm
		out = append(out, &cp)
	}

	return out, nil
}

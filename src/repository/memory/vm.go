package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/kumori-sh/spacetrk/pkg/exception"
	vmdomain "github.com/kumori-sh/spacetrk/src/core/domain/vm"
)

// VMRepository is a thread-safe in-memory VM repository.
type VMRepository struct {
	mu     sync.RWMutex
	vms    map[string]*vmdomain.VM
	leases map[string]*vmdomain.Lease
}

func NewVMRepository() *VMRepository {
	return &VMRepository{
		vms:    make(map[string]*vmdomain.VM),
		leases: make(map[string]*vmdomain.Lease),
	}
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

func (r *VMRepository) AssignToChatIfAvailable(_ context.Context, vmID, chatID string, idleDeadlineAt *time.Time) (*vmdomain.VM, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	vm, ok := r.vms[vmID]
	if !ok {
		return nil, exception.NotFound("vm", vmID)
	}

	if !vm.IsAvailable() {
		return nil, exception.BadRequest("VM is not available")
	}

	vm.AssignTo(chatID)
	vm.IdleDeadlineAt = idleDeadlineAt
	now := time.Now().UTC()
	r.leases[vmID] = &vmdomain.Lease{
		ID:       vmID + ":" + now.Format(time.RFC3339Nano),
		ChatID:   chatID,
		VMID:     vmID,
		LeasedAt: now,
	}

	cp := *vm
	return &cp, nil
}

func (r *VMRepository) ReleaseActiveLeaseByVM(_ context.Context, vmID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	lease, ok := r.leases[vmID]
	if !ok {
		return nil
	}
	now := time.Now().UTC()
	lease.ReleasedAt = &now
	delete(r.leases, vmID)
	return nil
}

func (r *VMRepository) ListActiveLeasesByChat(_ context.Context, chatID string) ([]vmdomain.Lease, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]vmdomain.Lease, 0)
	for _, lease := range r.leases {
		if lease.ChatID != chatID || lease.ReleasedAt != nil {
			continue
		}
		out = append(out, *lease)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].LeasedAt.After(out[j].LeasedAt)
	})

	return out, nil
}

func (r *VMRepository) FindPreviousLeaseForChat(_ context.Context, chatID string) (*vmdomain.VM, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	type leaseEntry struct {
		lease *vmdomain.Lease
	}
	var entries []leaseEntry
	for _, lease := range r.leases {
		if lease.ChatID != chatID {
			continue
		}
		entries = append(entries, leaseEntry{lease: lease})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].lease.LeasedAt.After(entries[j].lease.LeasedAt)
	})

	for _, e := range entries {
		vm, ok := r.vms[e.lease.VMID]
		if !ok {
			continue
		}
		if vm.Status != vmdomain.StatusIdle && vm.Status != vmdomain.StatusReady {
			continue
		}
		if vm.ChatID != nil {
			continue
		}
		cp := *vm
		return &cp, nil
	}

	return nil, exception.NotFound("previous vm for chat", chatID)
}

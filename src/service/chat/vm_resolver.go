package chatsvc

import (
	"context"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// VMResolver resolves an available VM for a chat, resuming from snapshot if needed.
type VMResolver interface {
	ResolveVMForChat(ctx context.Context, chatID string) (string, error)
}

// VMResolutionService is the subset of vm.Service needed for resolution.
type VMResolutionService interface {
	GetByChatID(ctx context.Context, chatID string) (*vmdomain.VM, error)
	FindPreviousLeaseForChat(ctx context.Context, chatID string) (*vmdomain.VM, error)
	HasSnapshot(ctx context.Context, vmID string) bool
	ResumeVM(ctx context.Context, vmID, chatID string) (*vmdomain.VM, error)
	AssignToChat(ctx context.Context, vmID, chatID string) (*vmdomain.VM, error)
}

type vmResolver struct {
	svc VMResolutionService
}

// NewVMResolver creates a VMResolver backed by the given VM resolution service.
func NewVMResolver(svc VMResolutionService) VMResolver {
	return &vmResolver{svc: svc}
}

// ResolveVMForChat finds or resumes a VM for the given chat.
// Returns the VM ID if one is available, or empty string if none exists.
func (r *vmResolver) ResolveVMForChat(ctx context.Context, chatID string) (string, error) {
	logger := pkglog.FromContext(ctx)

	// 1. Check if a VM is currently assigned to this chat and still active.
	if vm, err := r.svc.GetByChatID(ctx, chatID); err == nil && vm != nil {
		if vm.Status.IsActive() {
			logger.DebugContext(ctx, "VM resolver: found active VM assigned to chat", "vm_id", vm.ID, "chat_id", chatID)
			return vm.ID, nil
		}
	}

	// 2. Look for a previous lease that can be reused.
	prev, err := r.svc.FindPreviousLeaseForChat(ctx, chatID)
	if err != nil || prev == nil {
		logger.DebugContext(ctx, "VM resolver: no previous VM found for chat", "chat_id", chatID)
		return "", nil
	}

	// 3. If the previous VM is already active, reassign it.
	if prev.Status.IsActive() {
		assigned, err := r.svc.AssignToChat(ctx, prev.ID, chatID)
		if err != nil {
			logger.WarnContext(ctx, "VM resolver: failed to reassign active VM", "vm_id", prev.ID, "error", err)
			return "", nil
		}
		logger.InfoContext(ctx, "VM resolver: reassigned active VM to chat", "vm_id", assigned.ID, "chat_id", chatID)
		return assigned.ID, nil
	}

	// 4. If the VM has a snapshot, resume from it.
	if r.svc.HasSnapshot(ctx, prev.ID) {
		resumed, err := r.svc.ResumeVM(ctx, prev.ID, chatID)
		if err != nil {
			logger.WarnContext(ctx, "VM resolver: failed to resume VM from snapshot", "vm_id", prev.ID, "error", err)
			return "", nil
		}
		logger.InfoContext(ctx, "VM resolver: resumed VM from snapshot for chat", "vm_id", resumed.ID, "chat_id", chatID)
		return resumed.ID, nil
	}

	// 5. Previous VM exists but can't be resumed — no usable VM.
	logger.DebugContext(ctx, "VM resolver: previous VM found but not resumable", "vm_id", prev.ID, "status", string(prev.Status))
	return "", nil
}

// Compile-time check that vmResolver satisfies VMResolver.
var _ VMResolver = (*vmResolver)(nil)

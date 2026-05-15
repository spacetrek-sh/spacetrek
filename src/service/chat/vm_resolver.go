package chatsvc

import (
	"context"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/environment"
	"github.com/spacetrek-sh/spacetrek/src/core/ports"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// AvailableVMCollector gathers VMs available for the LLM to resume.
type AvailableVMCollector interface {
	CollectAvailableVMs(ctx context.Context, chatID string) ([]ports.AvailableVM, error)
}

// VMCollectionService is the subset of vm.Service needed for collecting available VMs.
type VMCollectionService interface {
	GetByChatID(ctx context.Context, chatID string) (*vmdomain.VM, error)
	ListPreviousLeasesForChat(ctx context.Context, chatID string) ([]*vmdomain.VM, error)
	HasSnapshot(ctx context.Context, vmID string) bool
}

type vmCollector struct {
	vmSvc  VMCollectionService
	envRepo environment.Repository
}

// NewAvailableVMCollector creates an AvailableVMCollector backed by the given services.
func NewAvailableVMCollector(vmSvc VMCollectionService, envRepo environment.Repository) AvailableVMCollector {
	return &vmCollector{vmSvc: vmSvc, envRepo: envRepo}
}

// CollectAvailableVMs returns all resumable VMs for a chat with environment metadata.
func (c *vmCollector) CollectAvailableVMs(ctx context.Context, chatID string) ([]ports.AvailableVM, error) {
	logger := pkglog.FromContext(ctx)

	// Collect active VM (currently assigned and running).
	var vms []*vmdomain.VM
	if active, err := c.vmSvc.GetByChatID(ctx, chatID); err == nil && active != nil && active.Status.IsActive() {
		vms = append(vms, active)
	}

	// Collect previous leases that can be resumed.
	prev, err := c.vmSvc.ListPreviousLeasesForChat(ctx, chatID)
	if err != nil {
		logger.WarnContext(ctx, "failed to list previous leases", "chat_id", chatID, "error", err)
	} else {
		vms = append(vms, prev...)
	}

	if len(vms) == 0 {
		return nil, nil
	}

	// Deduplicate by VM ID (active VM might also appear in previous leases).
	seen := make(map[string]struct{})
	result := make([]ports.AvailableVM, 0, len(vms))
	for _, vm := range vms {
		if _, ok := seen[vm.ID]; ok {
			continue
		}
		seen[vm.ID] = struct{}{}

		env, envErr := c.envRepo.GetByID(ctx, vm.EnvironmentID)
		envType := ""
		envDesc := ""
		if envErr == nil && env != nil {
			envType = string(env.Type)
			envDesc = env.Description
		}

		result = append(result, ports.AvailableVM{
			VMID:           vm.ID,
			Environment:    envType,
			EnvDescription: envDesc,
			Status:         string(vm.Status),
			HasSnapshot:    c.vmSvc.HasSnapshot(ctx, vm.ID),
		})
	}

	return result, nil
}

// Compile-time checks.
var (
	_ AvailableVMCollector = (*vmCollector)(nil)
)

// EnvironmentHintResolver resolves the environment description for a VM.
type EnvironmentHintResolver interface {
	ResolveEnvironmentHint(ctx context.Context, vmID string) (string, error)
}

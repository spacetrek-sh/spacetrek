// Package vm provides the VM service for VM management.
package vm

import (
	"context"

	"github.com/kumori-sh/spacetrk/pkg/exception"
	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	"github.com/kumori-sh/spacetrk/src/core/domain/environment"
	vmdomain "github.com/kumori-sh/spacetrk/src/core/domain/vm"
)

// Service handles VM business logic.
type Service struct {
	repo    vmdomain.Repository
	backend vmdomain.Backend // VM provider (Firecracker, etc.)
	envRepo EnvironmentRepository
}

// EnvironmentRepository defines the interface for fetching environment details.
type EnvironmentRepository interface {
	GetByID(ctx context.Context, id string) (*environment.Environment, error)
}

// NewService creates a new VM service.
func NewService(repo vmdomain.Repository, backend vmdomain.Backend, envRepo EnvironmentRepository) *Service {
	return &Service{
		repo:    repo,
		backend: backend,
		envRepo: envRepo,
	}
}

// Create provisions a new VM instance for the given environment.
func (s *Service) Create(ctx context.Context, envID string, provider vmdomain.Provider, vcpu, memoryMB, diskMB *int) (*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)

	// Validate environment exists
	env, err := s.envRepo.GetByID(ctx, envID)
	if err != nil {
		logger.WarnContext(ctx, "environment not found", "env_id", envID, "error", err)
		return nil, exception.NotFound("environment", envID)
	}

	// Apply default provider if not specified
	if provider == "" {
		provider = vmdomain.ProviderFirecracker
	}

	// Create VM entity with optional resource overrides
	vm := vmdomain.New(vmdomain.CreateParams{
		EnvironmentID: envID,
		Provider:      provider,
		VCPU:          vcpu,
		MemoryMB:      memoryMB,
		DiskMB:        diskMB,
	})

	// Persist VM to database
	if err := s.repo.Create(ctx, vm); err != nil {
		logger.ErrorContext(ctx, "failed to persist VM", "env_id", envID, "error", err)
		return nil, err
	}

	// Compute effective resources (use override if set, otherwise environment default)
	effectiveVCPU := vm.GetVCPU(env.GetVCPU())
	effectiveMemory := vm.GetMemoryMB(env.GetMemoryMB())
	effectiveDisk := vm.GetDiskMB(env.GetDiskMB())

	// Provision the VM via backend provider with effective resources
	spec := vmdomain.CreateSpec{
		EnvironmentID: env.ID,
		ImagePath:     env.ImagePath,
		Resources: environment.ResourceLimits{
			VCPU:     effectiveVCPU,
			MemoryMB: effectiveMemory,
			DiskMB:   effectiveDisk,
		},
		Runtime: vmdomain.DefaultRuntimeConfig(),
	}

	backendID, err := s.backend.Create(ctx, spec)
	if err != nil {
		logger.ErrorContext(ctx, "backend provisioning failed", "vm_id", vm.ID, "error", err)
		// Mark VM as terminated on failure
		vm.Terminate()
		s.repo.Update(ctx, vm)
		return nil, exception.Internal(err)
	}

	// Update VM with provider-assigned ID and mark as ready
	vm.Status = vmdomain.StatusReady
	if err := s.repo.Update(ctx, vm); err != nil {
		logger.ErrorContext(ctx, "failed to update VM status", "vm_id", vm.ID, "error", err)
		return nil, err
	}

	logger.InfoContext(ctx, "VM provisioned", "vm_id", vm.ID, "backend_id", backendID, "provider", provider)
	return vm, nil
}

// Get retrieves details of the specified VM.
func (s *Service) Get(ctx context.Context, id string) (*vmdomain.VM, error) {
	return s.repo.GetByID(ctx, id)
}

// GetAvailable retrieves an available VM from the pool for the given provider.
func (s *Service) GetAvailable(ctx context.Context, provider vmdomain.Provider) (*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)

	vms, err := s.repo.GetAvailablePool(ctx, provider, 1)
	if err != nil {
		logger.WarnContext(ctx, "failed to query VM pool", "provider", provider, "error", err)
		return nil, err
	}

	if len(vms) == 0 {
		return nil, exception.NotFound("available VM", string(provider))
	}

	return vms[0], nil
}

// AssignToChat assigns a VM to a chat/session.
func (s *Service) AssignToChat(ctx context.Context, vmID, chatID string) (*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)

	vm, err := s.repo.GetByID(ctx, vmID)
	if err != nil {
		logger.WarnContext(ctx, "VM not found", "vm_id", vmID, "error", err)
		return nil, err
	}

	if !vm.IsAvailable() {
		return nil, exception.BadRequest("VM is not available")
	}

	vm.AssignTo(chatID)
	if err := s.repo.Update(ctx, vm); err != nil {
		logger.ErrorContext(ctx, "failed to assign VM to chat", "vm_id", vmID, "chat_id", chatID, "error", err)
		return nil, err
	}

	logger.InfoContext(ctx, "VM assigned to chat", "vm_id", vmID, "chat_id", chatID)
	return vm, nil
}

// Unassign releases a VM from its current chat.
func (s *Service) Unassign(ctx context.Context, vmID string) (*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)

	vm, err := s.repo.GetByID(ctx, vmID)
	if err != nil {
		logger.WarnContext(ctx, "VM not found", "vm_id", vmID, "error", err)
		return nil, err
	}

	vm.Unassign()
	if err := s.repo.Update(ctx, vm); err != nil {
		logger.ErrorContext(ctx, "failed to unassign VM", "vm_id", vmID, "error", err)
		return nil, err
	}

	logger.InfoContext(ctx, "VM unassigned", "vm_id", vmID)
	return vm, nil
}

// Stop gracefully stops the specified VM.
func (s *Service) Stop(ctx context.Context, id string) (*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)

	vm, err := s.repo.GetByID(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "VM not found", "vm_id", id, "error", err)
		return nil, err
	}

	// Stop via backend
	if err := s.backend.Stop(ctx, id); err != nil {
		logger.ErrorContext(ctx, "backend stop failed", "vm_id", id, "error", err)
		return nil, exception.Internal(err)
	}

	// Release from chat and update status
	vm.Unassign()
	if err := s.repo.Update(ctx, vm); err != nil {
		logger.ErrorContext(ctx, "failed to update VM status", "vm_id", id, "error", err)
		return nil, err
	}

	logger.InfoContext(ctx, "VM stopped", "vm_id", id)
	return vm, nil
}

// Destroy permanently terminates and removes the specified VM.
func (s *Service) Destroy(ctx context.Context, id string) error {
	logger := pkglog.FromContext(ctx)

	vm, err := s.repo.GetByID(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "VM not found", "vm_id", id, "error", err)
		return err
	}

	// Destroy via backend
	if err := s.backend.Destroy(ctx, id); err != nil {
		logger.ErrorContext(ctx, "backend destroy failed", "vm_id", id, "error", err)
		return exception.Internal(err)
	}

	// Mark VM as terminated
	vm.Terminate()
	if err := s.repo.Update(ctx, vm); err != nil {
		logger.ErrorContext(ctx, "failed to update VM status", "vm_id", id, "error", err)
		return err
	}

	// Delete from database
	if err := s.repo.Delete(ctx, id); err != nil {
		logger.ErrorContext(ctx, "failed to delete VM", "vm_id", id, "error", err)
		return err
	}

	logger.InfoContext(ctx, "VM destroyed", "vm_id", id)
	return nil
}

// ExecuteCommand executes a command on the specified VM.
func (s *Service) ExecuteCommand(ctx context.Context, id, command string) (string, error) {
	logger := pkglog.FromContext(ctx)

	vm, err := s.repo.GetByID(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "VM not found", "vm_id", id, "error", err)
		return "", err
	}

	if !vm.Status.IsActive() {
		return "", exception.BadRequest("VM is not active")
	}

	// Execute via backend
	stdout, _, exitCode, err := s.backend.Execute(ctx, id, []string{"/bin/sh", "-c", command})
	if err != nil {
		logger.ErrorContext(ctx, "command execution failed", "vm_id", id, "exit_code", exitCode, "error", err)
		return "", exception.Internal(err)
	}

	logger.DebugContext(ctx, "command executed", "vm_id", id, "command", command, "exit_code", exitCode)
	return stdout, nil
}

// GetMetrics returns resource usage metrics for the specified VM.
func (s *Service) GetMetrics(ctx context.Context, id string) (vmdomain.Metrics, error) {
	logger := pkglog.FromContext(ctx)

	vm, err := s.repo.GetByID(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "VM not found", "vm_id", id, "error", err)
		return vmdomain.Metrics{}, err
	}

	metrics, err := s.backend.GetMetrics(ctx, vm.ID)
	if err != nil {
		logger.ErrorContext(ctx, "failed to get metrics", "vm_id", id, "error", err)
		return vmdomain.Metrics{}, exception.Internal(err)
	}

	return metrics, nil
}

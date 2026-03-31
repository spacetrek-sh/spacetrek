// Package vm provides the VM service for VM management.
package vm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kumori-sh/spacetrk/pkg/exception"
	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	"github.com/kumori-sh/spacetrk/src/core/domain/environment"
	vmdomain "github.com/kumori-sh/spacetrk/src/core/domain/vm"
)

// Service handles VM business logic.
type Service struct {
	repo        vmdomain.Repository
	metricsRepo vmdomain.MetricsHistoryRepository
	backend     vmdomain.Backend // VM provider (Firecracker, etc.)
	envRepo     EnvironmentRepository
	idleTimeout time.Duration
}

// EnvironmentRepository defines the interface for fetching environment details.
type EnvironmentRepository interface {
	GetByID(ctx context.Context, id string) (*environment.Environment, error)
}

// NewService creates a new VM service.
func NewService(repo vmdomain.Repository, metricsRepo vmdomain.MetricsHistoryRepository, backend vmdomain.Backend, envRepo EnvironmentRepository, idleTimeout time.Duration) *Service {
	if idleTimeout <= 0 {
		idleTimeout = 5 * time.Minute
	}

	return &Service{
		repo:        repo,
		metricsRepo: metricsRepo,
		backend:     backend,
		envRepo:     envRepo,
		idleTimeout: idleTimeout,
	}
}

// StartMetricsCollector periodically captures and persists VM metrics samples.
func (s *Service) StartMetricsCollector(ctx context.Context, interval time.Duration) {
	logger := pkglog.FromContext(ctx)
	if s.metricsRepo == nil {
		logger.WarnContext(ctx, "VM metrics collector disabled: no metrics repository configured")
		return
	}
	if interval <= 0 {
		interval = 10 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.InfoContext(ctx, "VM metrics collector started", "interval", interval.String())
	// Capture one sample batch at startup for immediate history availability.
	s.collectAndPersistMetrics(ctx)

	for {
		select {
		case <-ctx.Done():
			logger.InfoContext(ctx, "VM metrics collector stopped")
			return
		case <-ticker.C:
			s.collectAndPersistMetrics(ctx)
		}
	}
}

// StartIdleReaper periodically stops VMs that passed their idle deadline.
func (s *Service) StartIdleReaper(ctx context.Context, interval time.Duration) {
	logger := pkglog.FromContext(ctx)
	if interval <= 0 {
		interval = time.Minute
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.InfoContext(ctx, "VM idle reaper started", "interval", interval.String(), "idle_timeout", s.idleTimeout.String())

	for {
		select {
		case <-ctx.Done():
			logger.InfoContext(ctx, "VM idle reaper stopped")
			return
		case <-ticker.C:
			s.reapIdleVMs(ctx)
		}
	}
}

// StartRuntimeReconciler synchronizes runtime-observed status into persisted VM state.
func (s *Service) StartRuntimeReconciler(ctx context.Context, interval time.Duration) {
	logger := pkglog.FromContext(ctx)
	if interval <= 0 {
		interval = 30 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.InfoContext(ctx, "VM runtime reconciler started", "interval", interval.String())
	// Run once immediately on startup to rehydrate runtime state.
	s.reconcileRuntimeStates(ctx)

	for {
		select {
		case <-ctx.Done():
			logger.InfoContext(ctx, "VM runtime reconciler stopped")
			return
		case <-ticker.C:
			s.reconcileRuntimeStates(ctx)
		}
	}
}

func (s *Service) reconcileRuntimeStates(ctx context.Context) {
	logger := pkglog.FromContext(ctx)
	now := time.Now().UTC()

	vms, err := s.repo.GetActiveVMs(ctx)
	if err != nil {
		logger.WarnContext(ctx, "VM runtime reconciler failed to list active VMs", "error", err)
		return
	}

	for _, vm := range vms {
		runtimeID := vm.ID
		if vm.RuntimeID != nil && *vm.RuntimeID != "" {
			runtimeID = *vm.RuntimeID
		}

		runtimeStatus, statusErr := s.backend.Status(ctx, runtimeID)
		if statusErr != nil {
			if strings.Contains(strings.ToLower(statusErr.Error()), "not found") {
				state := "stopped"
				vm.RuntimeState = &state
				vm.PID = nil
				vm.LastHeartbeatAt = &now
				vm.IdleDeadlineAt = nil
				if vm.Status != vmdomain.StatusTerminated {
					vm.Unassign()
				}
				if repoErr := s.repo.ReleaseActiveLeaseByVM(ctx, vm.ID); repoErr != nil {
					logger.WarnContext(ctx, "VM runtime reconciler failed to release lease for not-found VM", "vm_id", vm.ID, "error", repoErr)
				}
				if err := s.repo.Update(ctx, vm); err != nil {
					logger.WarnContext(ctx, "VM runtime reconciler failed to persist not-found state", "vm_id", vm.ID, "error", err)
				}
			}
			continue
		}

		vm.SetRuntimeMetadata(runtimeID, "", runtimeStatus.PID, runtimeStatus.State)
		if runtimeStatus.State == "stopped" || runtimeStatus.State == "terminated" {
			vm.PID = nil
			if vm.Status != vmdomain.StatusTerminated {
				vm.Unassign()
			}
		}

		if err := s.repo.Update(ctx, vm); err != nil {
			logger.WarnContext(ctx, "VM runtime reconciler failed to persist runtime state", "vm_id", vm.ID, "error", err)
		}
	}
}

func (s *Service) reapIdleVMs(ctx context.Context) {
	logger := pkglog.FromContext(ctx)
	now := time.Now().UTC()

	vms, err := s.repo.GetActiveVMs(ctx)
	if err != nil {
		logger.WarnContext(ctx, "VM idle reaper failed to list active VMs", "error", err)
		return
	}

	for _, vm := range vms {
		if vm.IdleDeadlineAt == nil || now.Before(*vm.IdleDeadlineAt) {
			continue
		}

		if err := s.backend.Stop(ctx, vm.ID); err != nil {
			// If VM is not found in backend, it was already stopped/cleaned up.
			// Reconcile the database state to reflect reality instead of failing.
			if strings.Contains(strings.ToLower(err.Error()), "not found") {
				vm.Unassign()
				state := "stopped"
				vm.RuntimeState = &state
				vm.PID = nil
				vm.IdleDeadlineAt = nil
				vm.LastHeartbeatAt = &now

				if repoErr := s.repo.ReleaseActiveLeaseByVM(ctx, vm.ID); repoErr != nil {
					logger.WarnContext(ctx, "VM idle reaper failed to release lease for already-stopped VM", "vm_id", vm.ID, "error", repoErr)
				}
				if repoErr := s.repo.Update(ctx, vm); repoErr != nil {
					logger.WarnContext(ctx, "VM idle reaper failed to persist state for already-stopped VM", "vm_id", vm.ID, "error", repoErr)
				} else {
					logger.InfoContext(ctx, "VM idle reaper reconciled already-stopped VM", "vm_id", vm.ID)
				}
				continue
			}
			logger.WarnContext(ctx, "VM idle reaper failed to stop VM", "vm_id", vm.ID, "error", err)
			continue
		}

		vm.Unassign()
		state := "stopped"
		vm.RuntimeState = &state
		vm.PID = nil
		vm.IdleDeadlineAt = nil
		nowCopy := now
		vm.LastHeartbeatAt = &nowCopy

		if err := s.repo.ReleaseActiveLeaseByVM(ctx, vm.ID); err != nil {
			logger.WarnContext(ctx, "VM idle reaper failed to release VM lease", "vm_id", vm.ID, "error", err)
		}
		if err := s.repo.Update(ctx, vm); err != nil {
			logger.WarnContext(ctx, "VM idle reaper failed to persist VM state", "vm_id", vm.ID, "error", err)
			continue
		}

		logger.InfoContext(ctx, "VM auto-stopped due to idle timeout", "vm_id", vm.ID)
	}
}

func (s *Service) refreshIdleDeadline(vm *vmdomain.VM) {
	deadline := time.Now().UTC().Add(s.idleTimeout)
	vm.IdleDeadlineAt = &deadline
}

func (s *Service) collectAndPersistMetrics(ctx context.Context) {
	if s.metricsRepo == nil {
		return
	}

	logger := pkglog.FromContext(ctx)
	vms, err := s.ListRunningRuntimes(ctx)
	if err != nil {
		logger.WarnContext(ctx, "VM metrics collector failed to list running VMs", "error", err)
		return
	}

	for _, vm := range vms {
		metrics, metricsErr := s.GetMetrics(ctx, vm.ID)
		if metricsErr != nil {
			logger.DebugContext(ctx, "VM metrics collector failed to get metrics", "vm_id", vm.ID, "error", metricsErr)
			continue
		}

		point := vmdomain.MetricsPoint{
			VMID:                 vm.ID,
			CPUUsagePercent:      metrics.CPUUsagePercent,
			MemoryUsedMB:         metrics.MemoryUsedMB,
			MemoryLimitMB:        metrics.MemoryLimitMB,
			MemoryPercent:        metrics.MemoryPercent,
			DiskUsedMB:           metrics.DiskUsedMB,
			DiskLimitMB:          metrics.DiskLimitMB,
			DiskPercent:          metrics.DiskPercent,
			NetworkBytesSent:     metrics.NetworkBytesSent,
			NetworkBytesReceived: metrics.NetworkBytesReceived,
			CollectedAt:          time.Unix(metrics.CollectedAt, 0).UTC(),
		}
		if metrics.CollectedAt <= 0 {
			point.CollectedAt = time.Now().UTC()
		}

		if insertErr := s.metricsRepo.Insert(ctx, point); insertErr != nil {
			logger.WarnContext(ctx, "VM metrics collector failed to persist sample", "vm_id", vm.ID, "error", insertErr)
		}
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
		InstanceID:    vm.ID,
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

	// Persist runtime metadata for reconciliation.
	vm.SetRuntimeMetadata(backendID, "", 0, "created")
	if runtimeStatus, statusErr := s.backend.Status(ctx, backendID); statusErr == nil {
		vm.SetRuntimeMetadata(backendID, "", runtimeStatus.PID, runtimeStatus.State)
	}
	s.refreshIdleDeadline(vm)

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

// GetRuntimeSnapshot returns a VM with refreshed runtime-observed metadata.
func (s *Service) GetRuntimeSnapshot(ctx context.Context, id string) (*vmdomain.VM, error) {
	vm, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	runtimeID := vm.ID
	if vm.RuntimeID != nil && *vm.RuntimeID != "" {
		runtimeID = *vm.RuntimeID
	}

	runtimeStatus, statusErr := s.backend.Status(ctx, runtimeID)
	if statusErr != nil {
		if strings.Contains(strings.ToLower(statusErr.Error()), "not found") {
			state := "stopped"
			vm.RuntimeState = &state
			vm.PID = nil
			now := time.Now().UTC()
			vm.LastHeartbeatAt = &now
			if vm.Status != vmdomain.StatusTerminated {
				vm.Unassign()
			}
			if err := s.repo.Update(ctx, vm); err != nil {
				return nil, err
			}
			return vm, nil
		}
		return nil, statusErr
	}

	vm.SetRuntimeMetadata(runtimeID, "", runtimeStatus.PID, runtimeStatus.State)
	if runtimeStatus.State == "stopped" || runtimeStatus.State == "terminated" {
		vm.PID = nil
		if vm.Status != vmdomain.StatusTerminated {
			vm.Unassign()
		}
	}

	if err := s.repo.Update(ctx, vm); err != nil {
		return nil, err
	}

	return vm, nil
}

// ListRunningRuntimes returns active VMs that are currently running per provider status.
func (s *Service) ListRunningRuntimes(ctx context.Context) ([]*vmdomain.VM, error) {
	vms, err := s.repo.GetActiveVMs(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*vmdomain.VM, 0, len(vms))
	for _, vm := range vms {
		refreshed, refreshErr := s.GetRuntimeSnapshot(ctx, vm.ID)
		if refreshErr != nil {
			continue
		}
		if refreshed.RuntimeState == nil || strings.ToLower(*refreshed.RuntimeState) != "running" {
			continue
		}
		out = append(out, refreshed)
	}

	return out, nil
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

	deadline := time.Now().UTC().Add(s.idleTimeout)
	vm, err := s.repo.AssignToChatIfAvailable(ctx, vmID, chatID, &deadline)
	if err != nil {
		logger.ErrorContext(ctx, "failed to assign VM to chat", "vm_id", vmID, "chat_id", chatID, "error", err)
		return nil, err
	}

	logger.InfoContext(ctx, "VM assigned to chat", "vm_id", vmID, "chat_id", chatID)
	return vm, nil
}

// ListActiveLeasesByChat returns all active VM leases for a chat/session.
func (s *Service) ListActiveLeasesByChat(ctx context.Context, chatID string) ([]vmdomain.Lease, error) {
	return s.repo.ListActiveLeasesByChat(ctx, chatID)
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
	s.refreshIdleDeadline(vm)
	if err := s.repo.ReleaseActiveLeaseByVM(ctx, vmID); err != nil {
		logger.ErrorContext(ctx, "failed to release VM lease", "vm_id", vmID, "error", err)
		return nil, err
	}
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
	vm.IdleDeadlineAt = nil
	if err := s.repo.ReleaseActiveLeaseByVM(ctx, id); err != nil {
		logger.ErrorContext(ctx, "failed to release VM lease", "vm_id", id, "error", err)
		return nil, err
	}
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
	vm.IdleDeadlineAt = nil
	if err := s.repo.ReleaseActiveLeaseByVM(ctx, id); err != nil {
		logger.ErrorContext(ctx, "failed to release VM lease", "vm_id", id, "error", err)
		return err
	}
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
	s.refreshIdleDeadline(vm)
	if err := s.repo.Update(ctx, vm); err != nil {
		logger.WarnContext(ctx, "failed to refresh VM idle deadline after command", "vm_id", id, "error", err)
	}
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

// GetMetricsHistory returns persisted metrics samples for a VM.
func (s *Service) GetMetricsHistory(ctx context.Context, id string, from, to *time.Time, limit int) ([]vmdomain.MetricsPoint, error) {
	logger := pkglog.FromContext(ctx)

	if s.metricsRepo == nil {
		return nil, exception.Internal(fmt.Errorf("metrics history repository not configured"))
	}

	if _, err := s.repo.GetByID(ctx, id); err != nil {
		logger.WarnContext(ctx, "VM not found while loading metrics history", "vm_id", id, "error", err)
		return nil, err
	}

	points, err := s.metricsRepo.ListByVM(ctx, id, from, to, limit)
	if err != nil {
		logger.ErrorContext(ctx, "failed to get metrics history", "vm_id", id, "error", err)
		return nil, err
	}

	return points, nil
}

// Package vm provides the VM service for VM management.
package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/environment"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/snapshot"
	"github.com/spacetrek-sh/spacetrek/src/core/ports"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

const (
	executeReadinessMaxAttempts  = 8
	executeReadinessPollInterval = 350 * time.Millisecond
	executeMaxAttempts           = 3
	executeRetryInterval         = 450 * time.Millisecond
	executeLogPreviewLimit       = 512
)

// Service handles VM business logic.
type Service struct {
	repo          vmdomain.Repository
	metricsRepo   vmdomain.MetricsHistoryRepository
	snapRepo      snapshot.Repository
	backend       vmdomain.Backend // VM provider (Firecracker, etc.)
	envRepo       EnvironmentRepository
	snapshotStore ports.SnapshotStore // nil = local-only mode
	ipAllocator   *IPAllocator       // nil when networking disabled
	networkCfg    NetworkConfig      // zero-value when networking disabled
	idleTimeout   time.Duration
	autoSnapshot  bool
	resumeGrace   time.Duration
}

// NetworkConfig carries network parameters from app config to the VM service.
type NetworkConfig struct {
	BridgeName string
	Subnet     string
	GatewayIP  string
	DNSIP      string
}

// IPAllocator manages IP address allocation from a configured range.
type IPAllocator struct {
	repo    vmdomain.Repository
	ipStart net.IP
	ipEnd   net.IP
}

// NewIPAllocator creates an IPAllocator from the given range strings.
func NewIPAllocator(repo vmdomain.Repository, ipStart, ipEnd string) (*IPAllocator, error) {
	start := net.ParseIP(ipStart)
	if start == nil {
		return nil, fmt.Errorf("invalid ip_start: %s", ipStart)
	}
	end := net.ParseIP(ipEnd)
	if end == nil {
		return nil, fmt.Errorf("invalid ip_end: %s", ipEnd)
	}
	return &IPAllocator{repo: repo, ipStart: start, ipEnd: end}, nil
}

// Allocate finds the first free IP in the range and assigns it to the VM.
func (a *IPAllocator) Allocate(ctx context.Context, vmID string) (string, error) {
	allocated, err := a.repo.GetAllocatedIPs(ctx)
	if err != nil {
		return "", fmt.Errorf("get allocated ips: %w", err)
	}

	inUse := make(map[string]struct{}, len(allocated))
	for _, ip := range allocated {
		inUse[ip] = struct{}{}
	}

	for ip := a.ipStart; !ip.Equal(a.ipEnd); ip = nextIP(ip) {
		if _, used := inUse[ip.String()]; !used {
			if err := a.repo.SetIPAddress(ctx, vmID, ip.String()); err != nil {
				return "", fmt.Errorf("set ip address: %w", err)
			}
			return ip.String(), nil
		}
	}
	// Check the last IP too.
	if _, used := inUse[a.ipEnd.String()]; !used {
		if err := a.repo.SetIPAddress(ctx, vmID, a.ipEnd.String()); err != nil {
			return "", fmt.Errorf("set ip address: %w", err)
		}
		return a.ipEnd.String(), nil
	}

	return "", fmt.Errorf("no available IPs in range %s-%s", a.ipStart, a.ipEnd)
}

// Release clears the IP address for a VM.
func (a *IPAllocator) Release(ctx context.Context, vmID string) error {
	return a.repo.ReleaseIPAddress(ctx, vmID)
}

// nextIP increments an IPv4 address by 1.
func nextIP(ip net.IP) net.IP {
	result := make(net.IP, len(ip))
	copy(result, ip)
	for i := len(result) - 1; i >= 0; i-- {
		result[i]++
		if result[i] != 0 {
			break
		}
	}
	return result
}

// EnvironmentRepository defines the interface for fetching environment details.
type EnvironmentRepository interface {
	GetByID(ctx context.Context, id string) (*environment.Environment, error)
	List(ctx context.Context) ([]*environment.Environment, error)
}

// NewService creates a new VM service.
func NewService(repo vmdomain.Repository, metricsRepo vmdomain.MetricsHistoryRepository, backend vmdomain.Backend, envRepo EnvironmentRepository, snapRepo snapshot.Repository, snapshotStore ports.SnapshotStore, idleTimeout time.Duration, autoSnapshot bool, resumeGrace time.Duration, networkCfg NetworkConfig, ipAllocator *IPAllocator) *Service {
	if idleTimeout <= 0 {
		idleTimeout = 5 * time.Minute
	}
	if resumeGrace <= 0 {
		resumeGrace = 2 * time.Minute
	}

	return &Service{
		repo:          repo,
		metricsRepo:   metricsRepo,
		snapRepo:      snapRepo,
		backend:       backend,
		envRepo:       envRepo,
		snapshotStore: snapshotStore,
		ipAllocator:   ipAllocator,
		networkCfg:    networkCfg,
		idleTimeout:   idleTimeout,
		autoSnapshot:  autoSnapshot,
		resumeGrace:   resumeGrace,
	}
}

// ResolveEnvironment resolves an environment type name (e.g. "alpine", "ubuntu") to its UUID.
func (s *Service) ResolveEnvironment(ctx context.Context, envType string) (string, error) {
	envs, err := s.envRepo.List(ctx)
	if err != nil {
		return "", err
	}
	for _, env := range envs {
		if string(env.Type) == envType {
			return env.ID, nil
		}
	}
	return "", exception.NotFound("environment type", envType)
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
				vm.Terminate()
				if repoErr := s.repo.ReleaseActiveLeaseByVM(ctx, vm.ID); repoErr != nil {
					logger.WarnContext(ctx, "VM runtime reconciler failed to release lease for not-found VM", "vm_id", vm.ID, "error", repoErr)
				}
				if err := s.repo.Update(ctx, vm); err != nil {
					logger.WarnContext(ctx, "VM runtime reconciler failed to persist not-found state", "vm_id", vm.ID, "error", err)
				}
				logger.InfoContext(ctx, "VM runtime reconciler: terminated not-found VM", "vm_id", vm.ID)
			}
			continue
		}

		vm.SetRuntimeMetadata(runtimeID, "", runtimeStatus.PID, runtimeStatus.State)
		if runtimeStatus.State == "stopped" || runtimeStatus.State == "terminated" {
			vm.PID = nil
			vm.Terminate()
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

		// Skip recently-resumed VMs to give them a grace period.
		if vm.IsRecentlyResumed(s.resumeGrace) {
			continue
		}

		// Auto-snapshot before stopping if enabled.
		if s.autoSnapshot && s.snapRepo != nil {
			if _, snapErr := s.CreateSnapshot(ctx, vm.ID); snapErr != nil {
				logger.WarnContext(ctx, "VM idle reaper auto-snapshot failed, proceeding with stop", "vm_id", vm.ID, "error", snapErr)
			}
		}

		if err := s.backend.StopPreserving(ctx, vm.ID); err != nil {
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

	logger.DebugContext(ctx, "VM create: starting", "env_id", envID, "provider", provider)

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

	logger.DebugContext(ctx, "VM create: persisted to database", "vm_id", vm.ID)

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

	// Allocate IP and enable networking if configured.
	if s.ipAllocator != nil {
		ip, allocErr := s.ipAllocator.Allocate(ctx, vm.ID)
		if allocErr != nil {
			vm.Terminate()
			s.repo.Update(ctx, vm)
			return nil, exception.Internal(fmt.Errorf("allocate ip: %w", allocErr))
		}
		vm.IPAddress = &ip
		spec.Runtime.Network = vmdomain.NetworkConfig{
			Enabled:       true,
			IP:            ip,
			Bridge:        s.networkCfg.BridgeName,
			AllowInternet: true,
		}
		spec.Runtime.EnableNetworking = true
	}

	backendID, err := s.backend.Create(ctx, spec)
	if err != nil {
		logger.ErrorContext(ctx, "backend provisioning failed", "vm_id", vm.ID, "error", err)
		// Mark VM as terminated on failure
		vm.Terminate()
		s.repo.Update(ctx, vm)
		return nil, exception.Internal(err)
	}

	logger.DebugContext(ctx, "VM create: backend provisioned", "vm_id", vm.ID, "backend_id", backendID, "vcpu", effectiveVCPU, "memory_mb", effectiveMemory, "disk_mb", effectiveDisk)

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
	logger := pkglog.FromContext(ctx)
	logger.DebugContext(ctx, "VM get: fetching from repo", "vm_id", id)
	return s.repo.GetByID(ctx, id)
}

// GetByChatID retrieves the VM currently assigned to the given chat.
func (s *Service) GetByChatID(ctx context.Context, chatID string) (*vmdomain.VM, error) {
	return s.repo.GetByChatID(ctx, chatID)
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
	logger := pkglog.FromContext(ctx)

	vms, err := s.repo.GetActiveVMs(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*vmdomain.VM, 0, len(vms))
	for _, vm := range vms {
		refreshed, refreshErr := s.GetRuntimeSnapshot(ctx, vm.ID)
		if refreshErr != nil {
			logger.WarnContext(ctx, "ListRunningRuntimes: failed to snapshot VM", "vm_id", vm.ID, "error", refreshErr)
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

// AssignToChat assigns a VM to a chat.
func (s *Service) AssignToChat(ctx context.Context, vmID, chatID string) (*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)

	logger.DebugContext(ctx, "VM assign: starting", "vm_id", vmID, "chat_id", chatID)

	deadline := time.Now().UTC().Add(s.idleTimeout)
	vm, err := s.repo.AssignToChatIfAvailable(ctx, vmID, chatID, &deadline)
	if err != nil {
		logger.ErrorContext(ctx, "failed to assign VM to chat", "vm_id", vmID, "chat_id", chatID, "error", err)
		return nil, err
	}

	logger.InfoContext(ctx, "VM assigned to chat", "vm_id", vmID, "chat_id", chatID)
	return vm, nil
}

// ListActiveLeasesByChat returns all active VM leases for a chat.
func (s *Service) ListActiveLeasesByChat(ctx context.Context, chatID string) ([]vmdomain.Lease, error) {
	return s.repo.ListActiveLeasesByChat(ctx, chatID)
}

// FindPreviousLeaseForChat finds the most recent idle/ready VM previously leased to this chat.
func (s *Service) FindPreviousLeaseForChat(ctx context.Context, chatID string) (*vmdomain.VM, error) {
	return s.repo.FindPreviousLeaseForChat(ctx, chatID)
}

func (s *Service) ListPreviousLeasesForChat(ctx context.Context, chatID string) ([]*vmdomain.VM, error) {
	return s.repo.ListPreviousLeasesForChat(ctx, chatID)
}

// Unassign releases a VM from its current chat.
func (s *Service) Unassign(ctx context.Context, vmID string) (*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)

	logger.DebugContext(ctx, "VM unassign: starting", "vm_id", vmID)

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
// If auto-snapshot is enabled and the VM is running, a snapshot is created before stopping.
func (s *Service) Stop(ctx context.Context, id string) (*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)

	logger.DebugContext(ctx, "VM stop: starting", "vm_id", id)

	vm, err := s.repo.GetByID(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "VM not found", "vm_id", id, "error", err)
		return nil, err
	}

	// Auto-snapshot before stopping if enabled and VM is running.
	if s.autoSnapshot && vm.Status.IsActive() && s.snapRepo != nil {
		if _, snapErr := s.CreateSnapshot(ctx, id); snapErr != nil {
			logger.WarnContext(ctx, "auto-snapshot failed before stop, proceeding with stop", "vm_id", id, "error", snapErr)
		}
	}

	// Stop via backend (preserves rootfs for potential resume).
	if err := s.backend.StopPreserving(ctx, id); err != nil {
		logger.ErrorContext(ctx, "backend stop failed", "vm_id", id, "error", err)
		return nil, exception.Internal(err)
	}

	logger.DebugContext(ctx, "VM stop: backend stopped", "vm_id", id)

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

	logger.DebugContext(ctx, "VM destroy: starting", "vm_id", id)

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

	logger.DebugContext(ctx, "VM destroy: backend destroyed", "vm_id", id)

	// Mark VM as terminated
	vm.Terminate()
	vm.IdleDeadlineAt = nil
	if err := s.repo.ReleaseActiveLeaseByVM(ctx, id); err != nil {
		logger.ErrorContext(ctx, "failed to release VM lease", "vm_id", id, "error", err)
		return err
	}

	// Release allocated IP.
	if s.ipAllocator != nil {
		if err := s.ipAllocator.Release(ctx, id); err != nil {
			logger.WarnContext(ctx, "failed to release IP on destroy", "vm_id", id, "error", err)
		}
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
	commandPreview := logPreview(command, executeLogPreviewLimit)

	logger.DebugContext(ctx, "execute command requested", "vm_id", id, "command_len", len(command), "command_preview", commandPreview)

	vm, err := s.repo.GetByID(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "VM not found", "vm_id", id, "error", err)
		return "", err
	}

	if !vm.Status.IsActive() {
		return "", exception.BadRequest("VM is not active")
	}

	if err := s.waitForExecuteReadiness(ctx, id); err != nil {
		logger.WarnContext(ctx, "VM execute readiness check failed", "vm_id", id, "error", err)
		return "", err
	}

	var lastErr error
	for attempt := 1; attempt <= executeMaxAttempts; attempt++ {
		stdout, stderr, exitCode, execErr := s.backend.Execute(ctx, id, []string{"/bin/sh", "-c", command})
		if execErr == nil {
			logger.DebugContext(ctx, "command executed", "vm_id", id, "attempt", attempt, "exit_code", exitCode, "stdout_len", len(stdout), "stderr_len", len(stderr), "stdout_preview", logPreview(stdout, executeLogPreviewLimit), "stderr_preview", logPreview(stderr, executeLogPreviewLimit))
			s.refreshIdleDeadline(vm)
			if err := s.repo.Update(ctx, vm); err != nil {
				logger.WarnContext(ctx, "failed to refresh VM idle deadline after command", "vm_id", id, "error", err)
			}
			return stdout, nil
		}

		lastErr = execErr
		transient := isTransientExecuteError(execErr)
		if !transient || attempt == executeMaxAttempts {
			logger.ErrorContext(ctx, "command execution failed", "vm_id", id, "attempt", attempt, "exit_code", exitCode, "stdout_preview", logPreview(stdout, executeLogPreviewLimit), "stderr_preview", logPreview(stderr, executeLogPreviewLimit), "error", execErr)
			if transient {
				return "", exception.ServiceUnavailable("VM command channel is still initializing, retry shortly")
			}
			// Command ran but returned a non-zero exit code — surface output for debugging.
			if exitCode >= 0 {
				output := buildCommandErrorOutput(stdout, stderr, exitCode)
				s.refreshIdleDeadline(vm)
				if err := s.repo.Update(ctx, vm); err != nil {
					logger.WarnContext(ctx, "failed to refresh VM idle deadline after failed command", "vm_id", id, "error", err)
				}
				return output, exception.Internal(execErr)
			}
			return "", exception.Internal(execErr)
		}

		logger.DebugContext(ctx, "transient command execution failure; retrying", "vm_id", id, "attempt", attempt, "max_attempts", executeMaxAttempts, "retry_in", executeRetryInterval.String(), "error", execErr)
		if !sleepWithContext(ctx, executeRetryInterval) {
			return "", exception.ServiceUnavailable("VM command execution canceled while waiting to retry")
		}
	}

	if lastErr != nil {
		return "", exception.Internal(lastErr)
	}
	return "", exception.Internal(fmt.Errorf("command execution failed for unknown reason"))

}

func (s *Service) waitForExecuteReadiness(ctx context.Context, vmID string) error {
	logger := pkglog.FromContext(ctx)

	for attempt := 1; attempt <= executeReadinessMaxAttempts; attempt++ {
		status, err := s.backend.Status(ctx, vmID)
		if err == nil && isRuntimeReadyForExecute(status) {
			logger.DebugContext(ctx, "VM execute readiness confirmed", "vm_id", vmID, "attempt", attempt, "runtime_state", status.State, "pid", status.PID, "vsock_path", status.VsockPath, "guest_cid", status.GuestCID)
			return nil
		}

		reason := ""
		if err != nil {
			reason = err.Error()
		} else {
			reason = fmt.Sprintf("state=%s pid=%d vsock_path_set=%t guest_cid=%d", status.State, status.PID, status.VsockPath != "", status.GuestCID)
		}

		if attempt == executeReadinessMaxAttempts {
			logger.WarnContext(ctx, "VM execute readiness timed out", "vm_id", vmID, "attempts", executeReadinessMaxAttempts, "reason", reason)
			return exception.ServiceUnavailable("VM command channel is not ready yet, retry shortly")
		}

		logger.DebugContext(ctx, "VM not ready for execute yet", "vm_id", vmID, "attempt", attempt, "max_attempts", executeReadinessMaxAttempts, "reason", reason, "retry_in", executeReadinessPollInterval.String())
		if !sleepWithContext(ctx, executeReadinessPollInterval) {
			return exception.ServiceUnavailable("VM readiness check canceled")
		}
	}

	return exception.ServiceUnavailable("VM command channel is not ready yet, retry shortly")
}

func isRuntimeReadyForExecute(status vmdomain.RuntimeStatus) bool {
	state := strings.ToLower(strings.TrimSpace(status.State))
	return state == "running" && status.PID > 0 && status.VsockPath != "" && status.GuestCID > 0
}

func isTransientExecuteError(err error) bool {
	if err == nil {
		return false
	}

	message := strings.ToLower(err.Error())
	transientSubstrings := []string{
		"vsock command channel is not configured",
		"connect guest agent",
		"open guest vsock stream",
		"read execute response",
		"guest vsock connect failed",
		"agent_unavailable",
		"connection refused",
		"no such file or directory",
		"timeout",
	}

	for _, substr := range transientSubstrings {
		if strings.Contains(message, substr) {
			return true
		}
	}

	return false
}

func sleepWithContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func buildCommandErrorOutput(stdout, stderr string, exitCode int) string {
	var parts []string
	if stderr = strings.TrimSpace(stderr); stderr != "" {
		parts = append(parts, "stderr:\n"+stderr)
	}
	if stdout = strings.TrimSpace(stdout); stdout != "" {
		parts = append(parts, "stdout:\n"+stdout)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("command exited with code %d (no output)", exitCode)
	}
	return fmt.Sprintf("command exited with code %d\n%s", exitCode, strings.Join(parts, "\n"))
}

func logPreview(text string, limit int) string {
	normalized := strings.TrimSpace(text)
	if normalized == "" {
		return ""
	}

	normalized = strings.ReplaceAll(normalized, "\r", "\\r")
	normalized = strings.ReplaceAll(normalized, "\n", "\\n")

	if limit <= 0 || len(normalized) <= limit {
		return normalized
	}

	return normalized[:limit] + "...(truncated)"
}

// CreateSnapshot creates a full snapshot of the running VM.
// The VM remains running after the snapshot is taken.
func (s *Service) CreateSnapshot(ctx context.Context, vmID string) (*snapshot.Snapshot, error) {
	logger := pkglog.FromContext(ctx)

	vm, err := s.repo.GetByID(ctx, vmID)
	if err != nil {
		return nil, err
	}

	if !vm.Status.IsActive() {
		return nil, exception.BadRequest("VM is not active, cannot create snapshot")
	}

	env, err := s.envRepo.GetByID(ctx, vm.EnvironmentID)
	if err != nil {
		return nil, exception.NotFound("environment", vm.EnvironmentID)
	}

	// Build metadata capturing the VM spec at snapshot time.
	guestCID := uint32(0)
	if vm.GuestCID != nil {
		guestCID = *vm.GuestCID
	}
	guestPort := uint32(10789) // default guest agent port
	meta := snapshot.SnapshotMetadata{
		EnvironmentID: vm.EnvironmentID,
		ImagePath:     env.ImagePath,
		VCPU:          vm.GetVCPU(env.GetVCPU()),
		MemoryMB:      vm.GetMemoryMB(env.GetMemoryMB()),
		DiskMB:        vm.GetDiskMB(env.GetDiskMB()),
		GuestCID:      guestCID,
		GuestPort:     guestPort,
		RootfsPath:    filepath.Join("/var/lib/firecracker/vms", vm.ID, "rootfs.ext4"),
	}
	metaJSON, _ := json.Marshal(meta)

	// Call backend to create the snapshot.
	snapDir, sizeBytes, err := s.backend.CreateSnapshot(ctx, vmID)
	if err != nil {
		logger.ErrorContext(ctx, "backend snapshot failed", "vm_id", vmID, "error", err)
		return nil, exception.Internal(err)
	}

	snap := snapshot.New(snapshot.CreateParams{
		VMID:         vmID,
		Type:         snapshot.TypeFull,
		SnapshotPath: snapDir,
		SizeBytes:    sizeBytes,
		Metadata:     metaJSON,
	})

	// Upload snapshot files to object storage if configured.
	if s.snapshotStore != nil {
		s3Prefix := fmt.Sprintf("snapshots/%s/%s", vmID, snap.ID)
		files := []ports.SnapshotFile{
			{Key: s3Prefix + "/memory", LocalPath: snapDir + "/memory"},
			{Key: s3Prefix + "/state", LocalPath: snapDir + "/state"},
		}
		if uploaded, uploadErr := s.snapshotStore.UploadFiles(ctx, files); uploadErr != nil {
			logger.WarnContext(ctx, "failed to upload snapshot to storage, keeping local files", "snapshot_id", snap.ID, "error", uploadErr)
		} else {
			_ = os.RemoveAll(snapDir)
			snap.SnapshotPath = s3Prefix
			snap.SizeBytes = uploaded
			logger.InfoContext(ctx, "snapshot uploaded to storage", "snapshot_id", snap.ID, "s3_prefix", s3Prefix, "bytes", uploaded)
		}
	}

	if err := s.snapRepo.Create(ctx, snap); err != nil {
		logger.ErrorContext(ctx, "failed to persist snapshot record", "snapshot_id", snap.ID, "vm_id", vmID, "error", err)
		return nil, err
	}

	logger.InfoContext(ctx, "VM snapshot created", "vm_id", vmID, "snapshot_id", snap.ID, "size_bytes", sizeBytes)
	return snap, nil
}

// ResumeVM restores a VM from its latest snapshot and assigns it to a chat.
// If no snapshot exists, falls back to simple chat assignment.
func (s *Service) ResumeVM(ctx context.Context, vmID, chatID string) (*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)

	vm, err := s.repo.GetByID(ctx, vmID)
	if err != nil {
		return nil, err
	}

	// Try to find a snapshot for this VM.
	snap, snapErr := s.snapRepo.GetLatestByVMID(ctx, vmID)
	if snapErr != nil {
		// No snapshot — fall back to simple assignment if VM is available.
		logger.InfoContext(ctx, "no snapshot found, falling back to simple assignment", "vm_id", vmID, "error", snapErr)
		return s.AssignToChat(ctx, vmID, chatID)
	}

	meta, err := snap.ParseMetadata()
	if err != nil {
		return nil, exception.Internal(fmt.Errorf("failed to parse snapshot metadata: %w", err))
	}

	// Build CreateSpec from snapshot metadata to reconstruct the Firecracker machine.
	spec := vmdomain.CreateSpec{
		InstanceID:    vmID,
		EnvironmentID: meta.EnvironmentID,
		ImagePath:     meta.ImagePath,
		Resources: environment.ResourceLimits{
			VCPU:     meta.VCPU,
			MemoryMB: meta.MemoryMB,
			DiskMB:   meta.DiskMB,
		},
		Runtime: vmdomain.RuntimeConfig{
			Vsock: vmdomain.VsockConfig{
				Enabled:  true,
				GuestCID: meta.GuestCID,
				GuestPort: meta.GuestPort,
			},
		},
	}

	// Reuse the VM's existing IP for networking after restore.
	if vm.IPAddress != nil && *vm.IPAddress != "" && s.ipAllocator != nil {
		// Verify no other active VM holds this IP.
		allocated, checkErr := s.repo.GetAllocatedIPs(ctx)
		if checkErr == nil {
			for _, usedIP := range allocated {
				if usedIP == *vm.IPAddress {
					return nil, exception.Internal(fmt.Errorf("IP %s is already in use by another VM, cannot restore", *vm.IPAddress))
				}
			}
		}
		spec.Runtime.Network = vmdomain.NetworkConfig{
			Enabled:       true,
			IP:            *vm.IPAddress,
			Bridge:        s.networkCfg.BridgeName,
			AllowInternet: true,
		}
		spec.Runtime.EnableNetworking = true
	}

	// Restore the VM from snapshot.
	// If object storage is configured, download snapshot files to a local temp dir first.
	restoreDir := snap.SnapshotPath
	if s.snapshotStore != nil {
		tmpDir := filepath.Join("/var/lib/firecracker/vms", vmID, "snapshots", "restore-"+time.Now().UTC().Format("2006-01-02T15-04-05"))
		files := []ports.SnapshotFile{
			{Key: snap.MemFilePath(), LocalPath: tmpDir + "/memory"},
			{Key: snap.StateFilePath(), LocalPath: tmpDir + "/state"},
		}
		if err := s.snapshotStore.DownloadFiles(ctx, files); err != nil {
			return nil, exception.Internal(fmt.Errorf("failed to download snapshot from storage: %w", err))
		}
		defer os.RemoveAll(tmpDir)
		restoreDir = tmpDir
		logger.InfoContext(ctx, "downloaded snapshot from storage", "snapshot_id", snap.ID, "local_dir", tmpDir)
	}

	backendID, err := s.backend.RestoreFromSnapshot(ctx, spec, restoreDir)
	if err != nil {
		logger.ErrorContext(ctx, "failed to restore VM from snapshot", "vm_id", vmID, "snapshot_id", snap.ID, "error", err)
		return nil, exception.Internal(err)
	}

	// Update runtime metadata from the restored VM.
	vm.SetRuntimeMetadata(backendID, "", 0, "running")
	if runtimeStatus, statusErr := s.backend.Status(ctx, backendID); statusErr == nil {
		vm.SetRuntimeMetadata(backendID, "", runtimeStatus.PID, runtimeStatus.State)
		vm.SetRuntimeVsockMetadata(runtimeStatus.VsockPath, runtimeStatus.GuestCID)
	}

	vm.Status = vmdomain.StatusRunning
	vm.MarkResumed()
	s.refreshIdleDeadline(vm)

	// Assign to chat and create lease (single transactional operation).
	assignedVM, err := s.repo.AssignToChatIfAvailable(ctx, vmID, chatID, vm.IdleDeadlineAt)
	if err != nil {
		logger.ErrorContext(ctx, "failed to assign restored VM to chat", "vm_id", vmID, "chat_id", chatID, "error", err)
		return nil, err
	}

	// Persist runtime metadata updates.
	if err := s.repo.Update(ctx, vm); err != nil {
		logger.ErrorContext(ctx, "failed to update restored VM", "vm_id", vmID, "error", err)
		return nil, err
	}

	logger.InfoContext(ctx, "VM restored from snapshot", "vm_id", vmID, "chat_id", chatID, "snapshot_id", snap.ID)
	return assignedVM, nil
}

// HasSnapshot checks if a VM has any snapshots.
func (s *Service) HasSnapshot(ctx context.Context, vmID string) bool {
	if s.snapRepo == nil {
		return false
	}
	_, err := s.snapRepo.GetLatestByVMID(ctx, vmID)
	return err == nil
}

// GetMetrics returns resource usage metrics for the specified VM.
func (s *Service) GetMetrics(ctx context.Context, id string) (vmdomain.Metrics, error) {
	logger := pkglog.FromContext(ctx)

	logger.DebugContext(ctx, "VM metrics: fetching", "vm_id", id)

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

	logger.DebugContext(ctx, "VM metrics history: fetching", "vm_id", id, "limit", limit)

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

// Shutdown stops all active VMs and reconciles stale records before exit.
func (s *Service) Shutdown(ctx context.Context) {
	logger := pkglog.FromContext(ctx)

	vms, err := s.repo.GetActiveVMs(ctx)
	if err != nil {
		logger.ErrorContext(ctx, "shutdown: failed to list active VMs", "error", err)
		return
	}

	if len(vms) == 0 {
		logger.InfoContext(ctx, "shutdown: no active VMs to stop")
		return
	}

	logger.InfoContext(ctx, "shutdown: stopping active VMs", "count", len(vms))

	stopped, failed := 0, 0
	for _, vm := range vms {
		runtimeID := vm.ID
		if vm.RuntimeID != nil && *vm.RuntimeID != "" {
			runtimeID = *vm.RuntimeID
		}

		// Auto-snapshot before shutdown stop if enabled.
		if s.autoSnapshot && vm.Status.IsActive() && s.snapRepo != nil {
			if _, snapErr := s.CreateSnapshot(ctx, runtimeID); snapErr != nil {
				logger.WarnContext(ctx, "shutdown: auto-snapshot failed", "vm_id", vm.ID, "error", snapErr)
			}
		}

		if err := s.backend.StopPreserving(ctx, runtimeID); err != nil {
			logger.WarnContext(ctx, "shutdown: backend stop failed", "vm_id", vm.ID, "error", err)
			failed++
		} else {
			stopped++
		}

		vm.Unassign()
		vm.IdleDeadlineAt = nil
		_ = s.repo.ReleaseActiveLeaseByVM(ctx, vm.ID)
		if err := s.repo.Update(ctx, vm); err != nil {
			logger.WarnContext(ctx, "shutdown: failed to update VM status", "vm_id", vm.ID, "error", err)
		}
	}

	logger.InfoContext(ctx, "shutdown: VM cleanup complete", "stopped", stopped, "failed", failed, "total", len(vms))
}

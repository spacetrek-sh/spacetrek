// Package vm provides the VM service for VM management.
package vm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/environment"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/snapshot"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
	"github.com/spacetrek-sh/spacetrek/src/core/ports"
	fcutil "github.com/spacetrek-sh/spacetrek/src/infrastructure/vm/firecracker"
)

const (
	executeReadinessMaxAttempts  = 12
	executeReadinessPollInterval = 500 * time.Millisecond
	executeMaxAttempts           = 3
	executeRetryInterval         = 450 * time.Millisecond
	executeLogPreviewLimit       = 512

	defaultWorkspaceSizeGB = 2
	workspaceMountPath     = "/workspace"
	workspaceBaseDir       = "/var/lib/firecracker/vms"
)

// Service handles VM business logic.
type Service struct {
	repo          vmdomain.Repository
	metricsRepo   vmdomain.MetricsHistoryRepository
	snapRepo      snapshot.Repository
	snapMetricsRepo snapshot.MetricsRepository
	backend       vmdomain.Backend // VM provider (Firecracker, etc.)
	envRepo       EnvironmentRepository
	snapshotStore ports.SnapshotStore // nil = local-only mode
	ipAllocator   *IPAllocator        // nil when networking disabled
	networkCfg    NetworkConfig       // zero-value when networking disabled
	idleTimeout   time.Duration
	autoSnapshot  bool
	resumeGrace   time.Duration

	// metricsCache holds the latest collected metrics per VM.
	// Written by collectAndPersistMetrics, read by SSE handlers via GetCachedMetrics.
	metricsMu    sync.RWMutex
	metricsCache map[string]vmdomain.Metrics
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
func NewService(repo vmdomain.Repository, metricsRepo vmdomain.MetricsHistoryRepository, backend vmdomain.Backend, envRepo EnvironmentRepository, snapRepo snapshot.Repository, snapMetricsRepo snapshot.MetricsRepository, snapshotStore ports.SnapshotStore, idleTimeout time.Duration, autoSnapshot bool, resumeGrace time.Duration, networkCfg NetworkConfig, ipAllocator *IPAllocator) *Service {
	if idleTimeout <= 0 {
		idleTimeout = 5 * time.Minute
	}
	if resumeGrace <= 0 {
		resumeGrace = 2 * time.Minute
	}

	return &Service{
		repo:            repo,
		metricsRepo:     metricsRepo,
		snapRepo:        snapRepo,
		snapMetricsRepo: snapMetricsRepo,
		backend:         backend,
		envRepo:         envRepo,
		snapshotStore:   snapshotStore,
		ipAllocator:     ipAllocator,
		networkCfg:    networkCfg,
		idleTimeout:   idleTimeout,
		autoSnapshot:  autoSnapshot,
		resumeGrace:   resumeGrace,
		metricsCache:  make(map[string]vmdomain.Metrics),
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

// ResolveEnvironmentHint returns the environment description for a VM's environment.
func (s *Service) ResolveEnvironmentHint(ctx context.Context, vmID string) (string, error) {
	vm, err := s.repo.GetByID(ctx, vmID)
	if err != nil {
		return "", err
	}
	env, err := s.envRepo.GetByID(ctx, vm.EnvironmentID)
	if err != nil {
		return "", err
	}
	return env.Description, nil
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

		if err := s.teardownWorkspace(ctx, vm, vm.ID); err != nil {
			logger.WarnContext(ctx, "VM idle reaper workspace teardown failed, proceeding", "vm_id", vm.ID, "error", err)
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

	freshCache := make(map[string]vmdomain.Metrics, len(vms))

	for _, vm := range vms {
		metrics, metricsErr := s.GetMetrics(ctx, vm.ID)
		if metricsErr != nil {
			logger.DebugContext(ctx, "VM metrics collector failed to get metrics", "vm_id", vm.ID, "error", metricsErr)
			s.metricsMu.RLock()
			if old, ok := s.metricsCache[vm.ID]; ok {
				freshCache[vm.ID] = old
			}
			s.metricsMu.RUnlock()
			continue
		}

		freshCache[vm.ID] = metrics

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

	s.metricsMu.Lock()
	s.metricsCache = freshCache
	s.metricsMu.Unlock()
}

// Create provisions a new VM instance for the given environment.
func (s *Service) Create(ctx context.Context, envID, conversationID string, provider vmdomain.Provider, workspaceSizeGB int, vcpu, memoryMB, diskMB *int) (*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)

	logger.DebugContext(ctx, "VM create: starting", "env_id", envID, "conversation_id", conversationID, "provider", provider, "workspace_size_gb", workspaceSizeGB)

	if strings.TrimSpace(conversationID) == "" {
		return nil, exception.BadRequest("conversation_id is required")
	}
	if workspaceSizeGB <= 0 {
		workspaceSizeGB = defaultWorkspaceSizeGB
	}

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
		EnvironmentID:   envID,
		ConversationID:  strings.TrimSpace(conversationID),
		Provider:        provider,
		WorkspaceSizeGB: workspaceSizeGB,
		VCPU:            vcpu,
		MemoryMB:        memoryMB,
		DiskMB:          diskMB,
	})
	vm.DiffSnapshotsEnabled = env.DiffSnapshots

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
		Workspace: vmdomain.WorkspaceConfig{
			ConversationID: vm.ConversationID,
			SizeGB:         vm.WorkspaceSizeGB,
		},
		
		Runtime: vmdomain.DefaultRuntimeConfig(),
	}
	spec.Runtime.EnableDiffSnapshots = env.DiffSnapshots

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

	if err := s.prepareWorkspaceImageFromStorage(ctx, vm.ID, vm.ConversationID); err != nil {
		vm.Terminate()
		_ = s.repo.Update(ctx, vm)
		return nil, exception.Internal(fmt.Errorf("prepare workspace image: %w", err))
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

	if err := s.ensureWorkspaceMounted(ctx, backendID); err != nil {
		logger.ErrorContext(ctx, "workspace mount failed after create", "vm_id", vm.ID, "error", err)
		_ = s.backend.Destroy(ctx, backendID)
		vm.Terminate()
		_ = s.repo.Update(ctx, vm)
		return nil, err
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

// ListRunningRuntimesByUser returns running VMs that belong to the given user's chats.
func (s *Service) ListRunningRuntimesByUser(ctx context.Context, userID string) ([]*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)

	vms, err := s.repo.GetActiveByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}

	out := make([]*vmdomain.VM, 0, len(vms))
	for _, vm := range vms {
		refreshed, refreshErr := s.GetRuntimeSnapshot(ctx, vm.ID)
		if refreshErr != nil {
			logger.WarnContext(ctx, "ListRunningRuntimesByUser: failed to snapshot VM", "vm_id", vm.ID, "error", refreshErr)
			continue
		}
		if refreshed.RuntimeState == nil || strings.ToLower(*refreshed.RuntimeState) != "running" {
			continue
		}
		out = append(out, refreshed)
	}

	return out, nil
}

// FleetEntry is a VM paired with its latest cached metrics.
type FleetEntry struct {
	VM      *vmdomain.VM
	Metrics vmdomain.Metrics
}

// GetCachedMetrics returns the most recently collected metrics for a VM
// without hitting the backend. Returns the zero Metrics and false if no
// cached data is available.
func (s *Service) GetCachedMetrics(id string) (vmdomain.Metrics, bool) {
	s.metricsMu.RLock()
	m, ok := s.metricsCache[id]
	s.metricsMu.RUnlock()
	return m, ok
}

// ListCachedFleetSnapshot returns active VMs with their cached metrics.
// Uses a single DB query and reads from the metrics cache, avoiding
// per-VM backend calls.
func (s *Service) ListCachedFleetSnapshot(ctx context.Context, userID, role string) ([]FleetEntry, error) {
	var vms []*vmdomain.VM
	var err error
	if role == "admin" {
		vms, err = s.repo.GetActiveVMs(ctx)
	} else {
		vms, err = s.repo.GetActiveByUserID(ctx, userID)
	}
	if err != nil {
		return nil, err
	}

	s.metricsMu.RLock()
	defer s.metricsMu.RUnlock()

	out := make([]FleetEntry, 0, len(vms))
	for _, vm := range vms {
		metrics, _ := s.metricsCache[vm.ID]
		out = append(out, FleetEntry{VM: vm, Metrics: metrics})
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

// GetByEnvironmentAndChatID finds the most recent non-terminated VM with the given
// environment that was previously leased to the chat.
func (s *Service) GetByEnvironmentAndChatID(ctx context.Context, envID, chatID string) (*vmdomain.VM, error) {
	return s.repo.GetByEnvironmentAndChatID(ctx, envID, chatID)
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

	if err := s.teardownWorkspace(ctx, vm, id); err != nil {
		logger.WarnContext(ctx, "workspace teardown failed before stop, proceeding", "vm_id", id, "error", err)
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

	if err := s.teardownWorkspace(ctx, vm, id); err != nil {
		logger.WarnContext(ctx, "workspace teardown failed before destroy, proceeding", "vm_id", id, "error", err)
	}

	_ = s.deleteAllSnapshots(ctx, id)

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

// ReadFile reads a file from the guest VM, returning content in cat -n format.
func (s *Service) ReadFile(ctx context.Context, id, path string, offset, limit int) (string, error) {
	logger := pkglog.FromContext(ctx)

	vm, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return "", err
	}

	if !vm.Status.IsActive() {
		return "", exception.BadRequest("VM is not active")
	}

	if err := s.waitForExecuteReadiness(ctx, id); err != nil {
		return "", err
	}

	content, err := s.backend.ReadFile(ctx, id, path, offset, limit)
	if err != nil {
		logger.ErrorContext(ctx, "read file failed", "vm_id", id, "path", path, "error", err)
		return "", exception.Internal(err)
	}

	s.refreshIdleDeadline(vm)
	if err := s.repo.Update(ctx, vm); err != nil {
		logger.WarnContext(ctx, "failed to refresh VM idle deadline after read file", "vm_id", id, "error", err)
	}

	return content, nil
}

// WriteFile writes content to a file in the guest VM.
func (s *Service) WriteFile(ctx context.Context, id, path, content string, mode int) error {
	logger := pkglog.FromContext(ctx)

	vm, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}

	if !vm.Status.IsActive() {
		return exception.BadRequest("VM is not active")
	}

	if err := s.waitForExecuteReadiness(ctx, id); err != nil {
		return err
	}

	if err := s.backend.WriteFile(ctx, id, path, content, mode); err != nil {
		logger.ErrorContext(ctx, "write file failed", "vm_id", id, "path", path, "error", err)
		return exception.Internal(err)
	}

	s.refreshIdleDeadline(vm)
	if err := s.repo.Update(ctx, vm); err != nil {
		logger.WarnContext(ctx, "failed to refresh VM idle deadline after write file", "vm_id", id, "error", err)
	}

	return nil
}

// EditFile performs a surgical string replacement on a file in the guest VM.
func (s *Service) EditFile(ctx context.Context, id, path, oldString, newString string, replaceAll bool) error {
	logger := pkglog.FromContext(ctx)

	vm, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}

	if !vm.Status.IsActive() {
		return exception.BadRequest("VM is not active")
	}

	if err := s.waitForExecuteReadiness(ctx, id); err != nil {
		return err
	}

	if err := s.backend.EditFile(ctx, id, path, oldString, newString, replaceAll); err != nil {
		logger.ErrorContext(ctx, "edit file failed", "vm_id", id, "path", path, "error", err)
		return exception.Internal(err)
	}

	s.refreshIdleDeadline(vm)
	if err := s.repo.Update(ctx, vm); err != nil {
		logger.WarnContext(ctx, "failed to refresh VM idle deadline after edit file", "vm_id", id, "error", err)
	}

	return nil
}

func (s *Service) waitForExecuteReadiness(ctx context.Context, vmID string) error {
	logger := pkglog.FromContext(ctx)

	for attempt := 1; attempt <= executeReadinessMaxAttempts; attempt++ {
		status, err := s.backend.Status(ctx, vmID)
		if err != nil || !isRuntimeReadyForExecute(status) {
			reason := "unknown"
			if err != nil {
				reason = err.Error()
			} else {
				reason = fmt.Sprintf("state=%s pid=%d vsock_path_set=%t guest_cid=%d", status.State, status.PID, status.VsockPath != "", status.GuestCID)
			}

			if attempt == executeReadinessMaxAttempts {
				logger.WarnContext(ctx, "VM execute readiness timed out", "vm_id", vmID, "attempts", executeReadinessMaxAttempts, "reason", reason)
				return exception.ServiceUnavailable("VM command channel is not ready yet, retry shortly")
			}

			logger.DebugContext(ctx, "VM not ready for execute yet", "vm_id", vmID, "attempt", attempt, "reason", reason, "retry_in", executeReadinessPollInterval.String())
			if !sleepWithContext(ctx, executeReadinessPollInterval) {
				return exception.ServiceUnavailable("VM readiness check canceled")
			}
			continue
		}

		// Host-side state is running. Probe the guest agent via vsock to confirm
		// it has finished initializing (mount filesystems, start listener, etc.).
		probeCtx, probeCancel := context.WithTimeout(ctx, 3*time.Second)
		_, _, _, probeErr := s.backend.Execute(probeCtx, vmID, []string{"/bin/true"})
		probeCancel()
		if probeErr == nil {
			logger.DebugContext(ctx, "VM execute readiness confirmed (vsock probe OK)", "vm_id", vmID, "attempt", attempt)
			return nil
		}

		if attempt == executeReadinessMaxAttempts {
			logger.WarnContext(ctx, "VM execute readiness timed out (guest agent probe)", "vm_id", vmID, "attempts", executeReadinessMaxAttempts, "probe_error", probeErr)
			return exception.ServiceUnavailable("VM command channel is not ready yet, retry shortly")
		}

		logger.DebugContext(ctx, "VM host-side ready but guest agent not responding", "vm_id", vmID, "attempt", attempt, "probe_error", probeErr, "retry_in", executeReadinessPollInterval.String())
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

	// Determine snapshot type: incremental if a full snapshot exists and diff snapshots enabled.
	snapType := snapshot.TypeFull
	var parentID *string
	diffEnabled := vm.DiffSnapshotsEnabled

	if diffEnabled {
		existingFull, fullErr := s.snapRepo.GetLatestFull(ctx, vmID)
		if fullErr == nil && existingFull != nil {
			snapType = snapshot.TypeIncremental
			parentID = &existingFull.ID
			logger.InfoContext(ctx, "incremental snapshot: found base full snapshot", "vm_id", vmID, "base_snapshot_id", existingFull.ID)
		} else {
			// First snapshot is always full, subsequent ones will be incremental.
			logger.InfoContext(ctx, "no existing full snapshot, creating initial full snapshot", "vm_id", vmID)
		}
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
		DiffSnapshots: diffEnabled,
		BaseImagePath: env.ImagePath,
	}
	if parentID != nil {
		meta.BaseSnapshotID = *parentID
	}
	metaJSON, _ := json.Marshal(meta)

	// Call backend to create the snapshot.
	snapResult, err := s.backend.CreateSnapshot(ctx, vmID)
	if err != nil {
		logger.ErrorContext(ctx, "backend snapshot failed", "vm_id", vmID, "error", err)
		return nil, exception.Internal(err)
	}

	snapDir := snapResult.SnapshotDir
	sizeBytes := snapResult.MemoryBytes + snapResult.CowBytes // rough total for record

	snap := snapshot.New(snapshot.CreateParams{
		VMID:             vmID,
		ParentSnapshotID: parentID,
		Type:             snapType,
		SnapshotPath:     snapDir,
		SizeBytes:        sizeBytes,
		Metadata:         metaJSON,
	})

	// Upload snapshot files to object storage if configured.
	if s.snapshotStore != nil {
		s3Prefix := fmt.Sprintf("snapshots/%s/%s", vmID, snap.ID)

		// Compress snapshot files with zstd before upload.
		// This collapses sparse file zero-holes (CoW) and compresses
		// memory data, dramatically reducing S3 transfer and storage.
		snapshotFiles := []string{"memory", "state", "cow"}
		var files []ports.SnapshotFile
		for _, name := range snapshotFiles {
			localPath := filepath.Join(snapDir, name)
			if _, err := os.Stat(localPath); err != nil {
				continue // file doesn't exist (e.g. cow when dm-snapshot is off)
			}

			// For incremental snapshots, extract sparse regions from the
			// memory file BEFORE compression destroys sparseness.
			if name == "memory" && snapType == snapshot.TypeIncremental {
				regions, extractErr := fcutil.ExtractSparseRegions(localPath)
				if extractErr != nil {
					logger.WarnContext(ctx, "failed to extract sparse regions for manifest",
						"file", name, "error", extractErr)
				} else if len(regions) > 0 {
					manifestPath := localPath + ".manifest"
					if writeErr := fcutil.WriteManifest(manifestPath, regions); writeErr != nil {
						logger.WarnContext(ctx, "failed to write manifest", "file", name, "error", writeErr)
					} else {
						files = append(files, ports.SnapshotFile{
							Key:       s3Prefix + "/memory.manifest",
							LocalPath: manifestPath,
						})
						logger.InfoContext(ctx, "created memory manifest for incremental snapshot",
							"snapshot_id", snap.ID, "regions", len(regions))
					}
				}
			}

			zstPath := localPath + ".zst"
			if _, compErr := fcutil.CompressFileZstd(localPath, zstPath); compErr != nil {
				logger.WarnContext(ctx, "failed to compress snapshot file, uploading raw",
					"file", name, "error", compErr)
				files = append(files, ports.SnapshotFile{
					Key:       s3Prefix + "/" + name,
					LocalPath: localPath,
				})
			} else {
				files = append(files, ports.SnapshotFile{
					Key:       s3Prefix + "/" + name + ".zst",
					LocalPath: zstPath,
				})
			}
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

	metrics := &snapshot.SnapshotMetrics{
		SnapshotID:      snap.ID,
		VMID:            vmID,
		Type:            snapType,
		PauseDurationMs: snapResult.PauseDurationMs,
		MemoryBytes:     snapResult.MemoryBytes,
		CowBytes:        snapResult.CowBytes,
		CreatedAt:       time.Now().UTC(),
	}
	if err := s.snapMetricsRepo.Insert(ctx, metrics); err != nil {
		logger.WarnContext(ctx, "failed to persist snapshot metrics", "vm_id", vmID, "error", err)
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

	// If the latest snapshot is incremental, resolve the chain to find the
	// base full snapshot for memory merging. Chain depth is capped at 1.
	var baseSnap *snapshot.Snapshot
	if snap.IsIncremental() && snap.ParentSnapshotID != nil {
		baseSnap, err = s.snapRepo.GetByID(ctx, *snap.ParentSnapshotID)
		if err != nil {
			logger.WarnContext(ctx, "failed to resolve parent snapshot for chain merge, falling back to new VM",
				"vm_id", vmID, "parent_snapshot_id", *snap.ParentSnapshotID, "error", err)
			return s.Create(ctx, meta.EnvironmentID, chatID, vmdomain.ProviderFirecracker,
				vm.WorkspaceSizeGB, nil, nil, nil)
		}
		// Cap chain depth at 1: parent must be a full snapshot.
		if baseSnap != nil && baseSnap.IsIncremental() {
			logger.WarnContext(ctx, "parent is also incremental, chain too deep; falling back to new VM",
				"vm_id", vmID)
			return s.Create(ctx, meta.EnvironmentID, chatID, vmdomain.ProviderFirecracker,
				vm.WorkspaceSizeGB, nil, nil, nil)
		}
		logger.InfoContext(ctx, "resolved snapshot chain for restore",
			"vm_id", vmID, "base_snapshot_id", baseSnap.ID, "incremental_snapshot_id", snap.ID)
	}

	conversationID := strings.TrimSpace(vm.ConversationID)
	if conversationID == "" {
		conversationID = strings.TrimSpace(chatID)
		vm.ConversationID = conversationID
	}
	if vm.WorkspaceSizeGB <= 0 {
		vm.WorkspaceSizeGB = defaultWorkspaceSizeGB
	}
	if err := s.prepareWorkspaceImageFromStorage(ctx, vm.ID, conversationID); err != nil {
		return nil, exception.Internal(fmt.Errorf("prepare workspace image for resume: %w", err))
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
		Workspace: vmdomain.WorkspaceConfig{
			ConversationID: conversationID,
			SizeGB:         vm.WorkspaceSizeGB,
		},
		Runtime: vmdomain.RuntimeConfig{
			Vsock: vmdomain.VsockConfig{
				Enabled:   true,
				GuestCID:  meta.GuestCID,
				GuestPort: meta.GuestPort,
			},
		},
	}

	// Reuse the VM's existing IP for networking after restore.
	if vm.IPAddress != nil && *vm.IPAddress != "" && s.ipAllocator != nil {
		// Verify no other active VM holds this IP.
		allocated, checkErr := s.repo.GetAllocatedIPsExclude(ctx, vmID)
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

	resumeStart := time.Now().UTC()
	var downloadMs, restoreMs, agentReadyMs int64
	// Restore the VM from snapshot.
	// If object storage is configured, download snapshot files to a local temp dir first.
	restoreDir := snap.SnapshotPath
	restoreStart := time.Now().UTC()
	if s.snapshotStore != nil {
		tmpDir := filepath.Join("/var/lib/firecracker/vms", vmID, "snapshots", "restore-"+time.Now().UTC().Format("2006-01-02T15-04-05"))

		// Download snapshot files, trying compressed (.zst) first for
		// backward compatibility with older uncompressed snapshots.
		type dlEntry struct {
			name       string
			compressed bool
		}
		entries := []dlEntry{{name: "memory"}, {name: "state"}}
		if meta.DiffSnapshots {
			entries = append(entries, dlEntry{name: "cow"})
		}
		// For incremental snapshots, also download the memory manifest.
		if snap.IsIncremental() && baseSnap != nil {
			entries = append(entries, dlEntry{name: "memory.manifest"})
		}
		for i := range entries {
			zstKey := snap.SnapshotPath + "/" + entries[i].name + ".zst"
			if exists, _ := s.snapshotStore.ObjectExists(ctx, zstKey); exists {
				entries[i].compressed = true
			}
		}

		var files []ports.SnapshotFile
		for _, e := range entries {
			if e.compressed {
				files = append(files, ports.SnapshotFile{
					Key:       snap.SnapshotPath + "/" + e.name + ".zst",
					LocalPath: filepath.Join(tmpDir, e.name+".zst"),
				})
			} else {
				files = append(files, ports.SnapshotFile{
					Key:       snap.SnapshotPath + "/" + e.name,
					LocalPath: filepath.Join(tmpDir, e.name),
				})
			}
		}
		if err := s.snapshotStore.DownloadFiles(ctx, files); err != nil {
			return nil, exception.Internal(fmt.Errorf("failed to download snapshot from storage: %w", err))
		}
		downloadMs = time.Since(restoreStart).Milliseconds()

		// Decompress .zst files after download. Cow goes to VM dir, not tmpDir.
		for _, e := range entries {
			if e.compressed {
				zstPath := filepath.Join(tmpDir, e.name+".zst")
				rawPath := filepath.Join(tmpDir, e.name)
				if e.name == "cow" {
					rawPath = filepath.Join("/var/lib/firecracker/vms", vmID, "cow.img")
				}
				if err := fcutil.DecompressFileZstd(zstPath, rawPath); err != nil {
					os.RemoveAll(tmpDir)
					return nil, exception.Internal(fmt.Errorf("failed to decompress %s: %w", e.name, err))
				}
				_ = os.Remove(zstPath)
			} else if e.name == "cow" {
				src := filepath.Join(tmpDir, "cow")
				dst := filepath.Join("/var/lib/firecracker/vms", vmID, "cow.img")
				if err := os.Rename(src, dst); err != nil {
					return nil, exception.Internal(fmt.Errorf("failed to move cow to vm dir: %w", err))
				}
			}
		}

		// If incremental, download base memory and merge dirty regions,
		// and download the base snapshot's cow for disk chain reconstruction.
		if snap.IsIncremental() && baseSnap != nil {
			baseMemoryDir := filepath.Join(tmpDir, "base")
			if err := os.MkdirAll(baseMemoryDir, 0755); err != nil {
				return nil, exception.Internal(fmt.Errorf("create base snapshot dir: %w", err))
			}
			baseZstKey := baseSnap.SnapshotPath + "/memory.zst"
			baseRawKey := baseSnap.SnapshotPath + "/memory"
			baseZstLocal := filepath.Join(baseMemoryDir, "memory.zst")
			baseRawLocal := filepath.Join(baseMemoryDir, "memory")
			if exists, _ := s.snapshotStore.ObjectExists(ctx, baseZstKey); exists {
				if err := s.snapshotStore.DownloadFiles(ctx, []ports.SnapshotFile{
					{Key: baseZstKey, LocalPath: baseZstLocal},
				}); err != nil {
					return nil, exception.Internal(fmt.Errorf("download base memory: %w", err))
				}
				if err := fcutil.DecompressFileZstd(baseZstLocal, baseRawLocal); err != nil {
					return nil, exception.Internal(fmt.Errorf("decompress base memory: %w", err))
				}
				_ = os.Remove(baseZstLocal)
			} else {
				if err := s.snapshotStore.DownloadFiles(ctx, []ports.SnapshotFile{
					{Key: baseRawKey, LocalPath: baseRawLocal},
				}); err != nil {
					return nil, exception.Internal(fmt.Errorf("download base memory (raw): %w", err))
				}
			}
			manifestPath := filepath.Join(tmpDir, "memory.manifest")
			manifest, manifestErr := fcutil.ReadManifest(manifestPath)
			if manifestErr != nil {
				logger.WarnContext(ctx, "failed to read memory manifest, cannot merge chain; falling back to new VM",
					"error", manifestErr)
				return s.Create(ctx, meta.EnvironmentID, chatID, vmdomain.ProviderFirecracker,
					vm.WorkspaceSizeGB, nil, nil, nil)
			}
			diffMemoryPath := filepath.Join(tmpDir, "memory")
			mergedPath := filepath.Join(tmpDir, "memory.merged")
			logger.InfoContext(ctx, "merging incremental memory onto base",
				"vm_id", vmID, "regions", len(manifest))
			if mergeErr := fcutil.MergeDiffMemory(baseRawLocal, diffMemoryPath, mergedPath, manifest); mergeErr != nil {
				logger.WarnContext(ctx, "failed to merge diff memory; falling back to new VM",
					"error", mergeErr)
				return s.Create(ctx, meta.EnvironmentID, chatID, vmdomain.ProviderFirecracker,
					vm.WorkspaceSizeGB, nil, nil, nil)
			}
			if renameErr := os.Rename(mergedPath, diffMemoryPath); renameErr != nil {
				return nil, exception.Internal(fmt.Errorf("rename merged memory: %w", renameErr))
			}
			spec.RestoreAsFull = true
			logger.InfoContext(ctx, "memory chain merge complete", "vm_id", vmID)

			// Download the base (full) snapshot's cow file for disk chain
			// reconstruction. The incremental cow is already at cow.img;
			// rename it to cow_incr.img so we can place the base cow at
			// cow_full.img without conflict.
			vmDir := filepath.Join("/var/lib/firecracker/vms", vmID)
			cowIncrPath := filepath.Join(vmDir, "cow_incr.img")
			cowCurrentPath := filepath.Join(vmDir, "cow.img")
			if _, statErr := os.Stat(cowCurrentPath); statErr == nil {
				if renameErr := os.Rename(cowCurrentPath, cowIncrPath); renameErr != nil {
					return nil, exception.Internal(fmt.Errorf("rename incremental cow: %w", renameErr))
				}
			}

			cowFullPath := filepath.Join(vmDir, "cow_full.img")
			baseCowZstKey := baseSnap.SnapshotPath + "/cow.zst"
			baseCowRawKey := baseSnap.SnapshotPath + "/cow"
			baseCowZstLocal := filepath.Join(baseMemoryDir, "cow.zst")
			if exists, _ := s.snapshotStore.ObjectExists(ctx, baseCowZstKey); exists {
				if err := s.snapshotStore.DownloadFiles(ctx, []ports.SnapshotFile{
					{Key: baseCowZstKey, LocalPath: baseCowZstLocal},
				}); err != nil {
					return nil, exception.Internal(fmt.Errorf("download base cow: %w", err))
				}
				if err := fcutil.DecompressFileZstd(baseCowZstLocal, cowFullPath); err != nil {
					return nil, exception.Internal(fmt.Errorf("decompress base cow: %w", err))
				}
				_ = os.Remove(baseCowZstLocal)
			} else {
				if err := s.snapshotStore.DownloadFiles(ctx, []ports.SnapshotFile{
					{Key: baseCowRawKey, LocalPath: cowFullPath},
				}); err != nil {
					return nil, exception.Internal(fmt.Errorf("download base cow (raw): %w", err))
				}
			}

			spec.CowChainPaths = []string{cowFullPath, cowIncrPath}
			logger.InfoContext(ctx, "cow chain prepared for restore",
				"vm_id", vmID, "full_cow", cowFullPath, "incr_cow", cowIncrPath)
		}

		defer os.RemoveAll(tmpDir)
		restoreDir = tmpDir
		logger.InfoContext(ctx, "downloaded snapshot from storage", "snapshot_id", snap.ID, "local_dir", tmpDir)
	}

	// Enable diff snapshots on restore if the snapshot was taken with CoW.
	if meta.DiffSnapshots {
		spec.Runtime.EnableDiffSnapshots = true
	}

	restoreBackendStart := time.Now().UTC()
	backendID, err := s.backend.RestoreFromSnapshot(ctx, spec, restoreDir)
	if err != nil {
		logger.ErrorContext(ctx, "failed to restore VM from snapshot", "vm_id", vmID, "snapshot_id", snap.ID, "error", err)
		return nil, exception.Internal(err)
	}
	restoreMs = time.Since(restoreBackendStart).Milliseconds()

	agentStart := time.Now().UTC()
	if err := s.ensureWorkspaceMounted(ctx, backendID); err != nil {
		logger.ErrorContext(ctx, "workspace mount failed after restore", "vm_id", vmID, "error", err)
		_ = s.backend.StopPreserving(ctx, backendID)
		return nil, err
	}
	agentReadyMs = time.Since(agentStart).Milliseconds()

	// Update runtime metadata from the restored VM.
	vm.SetRuntimeMetadata(backendID, "", 0, "running")
	if runtimeStatus, statusErr := s.backend.Status(ctx, backendID); statusErr == nil {
		vm.SetRuntimeMetadata(backendID, "", runtimeStatus.PID, runtimeStatus.State)
		vm.SetRuntimeVsockMetadata(runtimeStatus.VsockPath, runtimeStatus.GuestCID)
	}

	vm.Status = vmdomain.StatusRunning
	vm.MarkResumed()
	s.refreshIdleDeadline(vm)

	// Persist status to DB before assigning so AssignToChatIfAvailable
	// sees the correct status instead of the pre-snapshot value.
	if err := s.repo.Update(ctx, vm); err != nil {
		logger.ErrorContext(ctx, "failed to update restored VM", "vm_id", vmID, "error", err)
		return nil, err
	}

	// Assign to chat and create lease (single transactional operation).
	assignedVM, err := s.repo.AssignToChatIfAvailable(ctx, vmID, chatID, vm.IdleDeadlineAt)
	if err != nil {
		logger.ErrorContext(ctx, "failed to assign restored VM to chat", "vm_id", vmID, "chat_id", chatID, "error", err)
		return nil, err
	}

	// Record resume metrics.
	if s.snapMetricsRepo != nil {
		totalMs := time.Since(resumeStart).Milliseconds()
		_ = s.snapMetricsRepo.Insert(ctx, &snapshot.SnapshotMetrics{
			SnapshotID:        snap.ID,
			VMID:              vmID,
			Type:              snap.Type,
			DownloadDurationMs: downloadMs,
			RestoreDurationMs: restoreMs,
			AgentReadyMs:      agentReadyMs,
			TotalResumeMs:     totalMs,
			GuestRAMMB:        meta.MemoryMB,
			CreatedAt:         time.Now().UTC(),
		})
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

		if err := s.teardownWorkspace(ctx, vm, runtimeID); err != nil {
			logger.WarnContext(ctx, "shutdown: workspace teardown failed, proceeding", "vm_id", vm.ID, "error", err)
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

func (s *Service) ensureWorkspaceMounted(ctx context.Context, runtimeID string) error {
	logger := pkglog.FromContext(ctx)
	logger.InfoContext(ctx, "ensureWorkspaceMounted: starting", "runtime_id", runtimeID)

	if err := s.waitForExecuteReadiness(ctx, runtimeID); err != nil {
		logger.ErrorContext(ctx, "ensureWorkspaceMounted: guest agent not ready", "runtime_id", runtimeID, "error", err)
		return err
	}

	// If a workspace archive was downloaded from S3, extract it into the
	// ext4 image on the HOST BEFORE the guest mounts it. This avoids:
	// 1) The old base64-in-shell-argument approach that broke for large archives
	// 2) Dual-mounting the same ext4 from two kernels (host + guest) which
	//    causes filesystem corruption.
	archivePath := workspaceArchivePath(runtimeID)
	archiveInfo, statErr := os.Stat(archivePath)
	if statErr == nil {
		if err := s.extractWorkspaceArchiveOnHost(ctx, runtimeID, archivePath, archiveInfo.Size()); err != nil {
			return err
		}
	} else {
		logger.InfoContext(ctx, "ensureWorkspaceMounted: no workspace archive found, skipping extraction",
			"runtime_id", runtimeID, "archive_path", archivePath)
	}

	// Now mount the workspace inside the guest. The readiness check already
	// confirmed vsock connectivity with a /bin/true probe, so the guest
	// agent is listening. A single mount attempt is sufficient.
	mountCmd := fmt.Sprintf("mkdir -p %s && if ! mountpoint -q %s; then mount -L workspace %s || mount /dev/vdb %s; fi", workspaceMountPath, workspaceMountPath, workspaceMountPath, workspaceMountPath)
	if _, _, exitCode, err := s.backend.Execute(ctx, runtimeID, []string{"/bin/sh", "-c", mountCmd}); err != nil {
		logger.ErrorContext(ctx, "ensureWorkspaceMounted: guest mount command failed",
			"runtime_id", runtimeID, "exit_code", exitCode, "error", err)
		return fmt.Errorf("mount workspace: %w (exit_code=%d)", err, exitCode)
	}
	logger.InfoContext(ctx, "ensureWorkspaceMounted: workspace mounted in guest", "runtime_id", runtimeID)

	return nil
}

// extractWorkspaceArchiveOnHost loop-mounts the workspace ext4 image on the
// host, extracts the tar.gz archive into it, then unmounts. This must be
// called BEFORE the guest mounts the same image to avoid dual-mount corruption.
func (s *Service) extractWorkspaceArchiveOnHost(ctx context.Context, runtimeID, archivePath string, archiveSize int64) error {
	logger := pkglog.FromContext(ctx)

	logger.InfoContext(ctx, "extractWorkspaceArchiveOnHost: workspace archive found",
		"runtime_id", runtimeID, "archive_path", archivePath, "archive_size_bytes", archiveSize)

	if archiveSize == 0 {
		logger.WarnContext(ctx, "extractWorkspaceArchiveOnHost: workspace archive is empty, skipping extraction",
			"runtime_id", runtimeID, "archive_path", archivePath)
		_ = os.Remove(archivePath)
		return nil
	}

	imagePath := workspaceImagePath(runtimeID)
	imageInfo, err := os.Stat(imagePath)
	if err != nil {
		logger.ErrorContext(ctx, "extractWorkspaceArchiveOnHost: workspace ext4 image not found",
			"runtime_id", runtimeID, "image_path", imagePath, "error", err)
		return fmt.Errorf("workspace image not found for host-side extraction: %w", err)
	}
	logger.InfoContext(ctx, "extractWorkspaceArchiveOnHost: ext4 image found",
		"runtime_id", runtimeID, "image_path", imagePath, "image_size_bytes", imageInfo.Size())

	hostMountDir := filepath.Join(workspaceBaseDir, runtimeID, "host_mnt")
	if err := os.MkdirAll(hostMountDir, 0755); err != nil {
		logger.ErrorContext(ctx, "extractWorkspaceArchiveOnHost: failed to create host mount dir",
			"runtime_id", runtimeID, "mount_dir", hostMountDir, "error", err)
		return fmt.Errorf("create host mount dir: %w", err)
	}
	defer os.Remove(hostMountDir)

	extractStart := time.Now()

	// Loop-mount the ext4 workspace image on the host.
	mountOut, err := exec.CommandContext(ctx, "mount", "-o", "loop", imagePath, hostMountDir).CombinedOutput()
	if err != nil {
		logger.ErrorContext(ctx, "extractWorkspaceArchiveOnHost: host loop-mount failed",
			"runtime_id", runtimeID, "image_path", imagePath, "mount_dir", hostMountDir,
			"output", string(mountOut), "error", err)
		return fmt.Errorf("host loop-mount workspace image: %w (output: %s)", err, string(mountOut))
	}
	logger.InfoContext(ctx, "extractWorkspaceArchiveOnHost: host loop-mount succeeded",
		"runtime_id", runtimeID, "image_path", imagePath, "mount_dir", hostMountDir)

	// Always unmount on the host side when done.
	defer func() {
		syncOut, syncErr := exec.CommandContext(ctx, "sync").CombinedOutput()
		if syncErr != nil {
			logger.WarnContext(ctx, "extractWorkspaceArchiveOnHost: sync failed",
				"runtime_id", runtimeID, "output", string(syncOut), "error", syncErr)
		}
		umountOut, umountErr := exec.CommandContext(ctx, "umount", hostMountDir).CombinedOutput()
		if umountErr != nil {
			logger.WarnContext(ctx, "extractWorkspaceArchiveOnHost: host umount failed, trying lazy umount",
				"runtime_id", runtimeID, "mount_dir", hostMountDir,
				"output", string(umountOut), "error", umountErr)
			lazyOut, lazyErr := exec.CommandContext(ctx, "umount", "-l", hostMountDir).CombinedOutput()
			if lazyErr != nil {
				logger.ErrorContext(ctx, "extractWorkspaceArchiveOnHost: host lazy umount also failed",
					"runtime_id", runtimeID, "output", string(lazyOut), "error", lazyErr)
			}
		} else {
			logger.InfoContext(ctx, "extractWorkspaceArchiveOnHost: host umount succeeded", "runtime_id", runtimeID)
		}
	}()

	// Extract the tar.gz archive directly into the loop-mounted workspace.
	tarOut, err := exec.CommandContext(ctx, "tar", "xzf", archivePath, "-C", hostMountDir).CombinedOutput()
	if err != nil {
		logger.ErrorContext(ctx, "extractWorkspaceArchiveOnHost: tar extraction failed",
			"runtime_id", runtimeID, "archive_path", archivePath,
			"archive_size_bytes", archiveSize, "mount_dir", hostMountDir,
			"output", string(tarOut), "error", err)
		return fmt.Errorf("extract workspace archive on host: %w (output: %s)", err, string(tarOut))
	}

	extractDuration := time.Since(extractStart)
	logger.InfoContext(ctx, "extractWorkspaceArchiveOnHost: workspace archive extracted successfully",
		"runtime_id", runtimeID, "archive_size_bytes", archiveSize,
		"extract_duration_ms", extractDuration.Milliseconds())

	_ = os.Remove(archivePath)
	return nil
}

func (s *Service) teardownWorkspace(ctx context.Context, vm *vmdomain.VM, runtimeID string) error {
	if vm == nil {
		return nil
	}
	if runtimeID == "" {
		runtimeID = vm.ID
	}

	var failures []string

	if err := s.uploadWorkspaceSnapshot(ctx, runtimeID, vm.ID, vm.ConversationID); err != nil {
		failures = append(failures, fmt.Sprintf("upload workspace snapshot: %v", err))
	}

	if err := s.syncAndUnmountWorkspace(ctx, runtimeID); err != nil {
		failures = append(failures, fmt.Sprintf("unmount workspace: %v", err))
	}

	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}

	return nil
}

func (s *Service) syncAndUnmountWorkspace(ctx context.Context, runtimeID string) error {
	if err := s.waitForExecuteReadiness(ctx, runtimeID); err != nil {
		return fmt.Errorf("workspace command channel not ready: %w", err)
	}

	umountCmd := fmt.Sprintf("sync && if mountpoint -q %s; then umount %s || umount -l %s; fi", workspaceMountPath, workspaceMountPath, workspaceMountPath)
	return s.executeGuestShellWithRetry(ctx, runtimeID, umountCmd, 2)
}

func (s *Service) executeGuestShellWithRetry(ctx context.Context, runtimeID, command string, attempts int) error {
	if attempts <= 0 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		_, _, _, err := s.backend.Execute(ctx, runtimeID, []string{"/bin/sh", "-c", command})
		if err == nil {
			return nil
		}

		lastErr = err
		if attempt == attempts || !isTransientExecuteError(err) {
			break
		}

		if !sleepWithContext(ctx, executeRetryInterval) {
			return exception.ServiceUnavailable("workspace command execution canceled")
		}
	}

	if lastErr != nil {
		return lastErr
	}

	return fmt.Errorf("workspace command failed")
}

// executeGuestShellWithRetryResult runs a shell command in the guest and returns stdout.
func (s *Service) executeGuestShellWithRetryResult(ctx context.Context, runtimeID, command string, attempts int) (string, error) {
	if attempts <= 0 {
		attempts = 1
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		stdout, _, _, err := s.backend.Execute(ctx, runtimeID, []string{"/bin/sh", "-c", command})
		if err == nil {
			return stdout, nil
		}

		lastErr = err
		if attempt == attempts || !isTransientExecuteError(err) {
			break
		}

		if !sleepWithContext(ctx, executeRetryInterval) {
			return "", exception.ServiceUnavailable("workspace command execution canceled")
		}
	}

	return "", lastErr
}

func (s *Service) prepareWorkspaceImageFromStorage(ctx context.Context, vmID, conversationID string) error {
	if s.snapshotStore == nil {
		return nil
	}

	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil
	}

	key := workspaceSnapshotKey(conversationID)
	exists, err := s.snapshotStore.ObjectExists(ctx, key)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	if err := os.MkdirAll(filepath.Join(workspaceBaseDir, vmID), 0755); err != nil {
		return fmt.Errorf("create workspace directory: %w", err)
	}

	tarPath := workspaceArchivePath(vmID)
	files := []ports.SnapshotFile{{Key: key, LocalPath: tarPath}}
	if err := s.snapshotStore.DownloadFiles(ctx, files); err != nil {
		return fmt.Errorf("download workspace archive %s: %w", key, err)
	}

	return nil
}

func (s *Service) uploadWorkspaceSnapshot(ctx context.Context, runtimeID, vmID, conversationID string) error {
	if s.snapshotStore == nil {
		return nil
	}

	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return fmt.Errorf("missing conversation_id for workspace snapshot")
	}

	imagePath := workspaceImagePath(vmID)
	if _, err := os.Stat(imagePath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat workspace image: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "workspace-upload-"+vmID)
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	tarPath := filepath.Join(tmpDir, "workspace.tar.gz")

	// Archive workspace contents from inside the guest via vsock.
	// Base64-encode because the vsock protocol uses JSON framing and can't carry raw binary.
	stdout, err := s.executeGuestShellWithRetryResult(ctx, runtimeID,
		"tar czf - -C /workspace --exclude=./lost+found . | base64 -w0", 2)
	if err != nil {
		return fmt.Errorf("archive workspace via guest: %w", err)
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stdout))
	if err != nil {
		return fmt.Errorf("decode base64 archive: %w", err)
	}

	if err := os.WriteFile(tarPath, decoded, 0644); err != nil {
		return fmt.Errorf("write archive: %w", err)
	}

	key := workspaceSnapshotKey(conversationID)
	files := []ports.SnapshotFile{{Key: key, LocalPath: tarPath}}
	if _, err := s.snapshotStore.UploadFiles(ctx, files); err != nil {
		return fmt.Errorf("upload workspace archive %s: %w", key, err)
	}

	return nil
}

func workspaceImagePath(vmID string) string {
	return filepath.Join(workspaceBaseDir, vmID, "workspace.ext4")
}

func workspaceSnapshotKey(conversationID string) string {
	return fmt.Sprintf("workspaces/%s.tar.gz", conversationID)
}

func workspaceArchivePath(vmID string) string {
	return filepath.Join(workspaceBaseDir, vmID, "workspace.tar.gz")
}

func (s *Service) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	snap, err := s.snapRepo.GetByID(ctx, snapshotID)
	if err != nil {
		return err
	}

	// Simple check for children manually since we don't have ListByParentID
	snaps, err := s.snapRepo.GetByVMID(ctx, snap.VMID)
	if err != nil {
		return err
	}
	
	for _, childSnap := range snaps {
		if childSnap.ParentSnapshotID != nil && *childSnap.ParentSnapshotID == snapshotID {
			return exception.BadRequest("cannot delete snapshot because it has children")
		}
	}

	if s.snapshotStore != nil {
		sPrefix := fmt.Sprintf("snapshots/%s/%s", snap.VMID, snap.ID)
		_ = s.snapshotStore.DeletePrefix(ctx, sPrefix)
	}
	
	// Assume backend snapshot cleanup is handled or unnecessary if files are removed.
	
	return s.snapRepo.Delete(ctx, snapshotID)
}

func (s *Service) deleteAllSnapshots(ctx context.Context, vmID string) error {
	if s.snapRepo == nil {
		return nil
	}
	
	snaps, err := s.snapRepo.GetByVMID(ctx, vmID)
	if err != nil {
		return err
	}

	// Delete in reverse order to respect simple dependencies (incremental after full)
	for i := len(snaps) - 1; i >= 0; i-- {
		snap := snaps[i]
		if s.snapshotStore != nil {
			sPrefix := fmt.Sprintf("snapshots/%s/%s", vmID, snap.ID)
			_ = s.snapshotStore.DeletePrefix(ctx, sPrefix)
		}
		_ = s.snapRepo.Delete(ctx, snap.ID)
	}

	return nil
}

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

	"github.com/lib/pq"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/environment"
	orchdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/orchestrator"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/snapshot"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/vm/naming"
	"github.com/spacetrek-sh/spacetrek/src/core/ports"
	fcutil "github.com/spacetrek-sh/spacetrek/src/infrastructure/vm/firecracker"
	"github.com/spacetrek-sh/spacetrek/src/service/vm/activitybroadcaster"
	"github.com/spacetrek-sh/spacetrek/src/service/vm/fleetbroadcaster"
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
	snapDiskCfg   SnapshotDiskConfig

	// hook is notified after VM lifecycle transitions (Create, Destroy). It's
	// used by the hostswriter to refresh dnsmasq's addn-hosts file. Set
	// post-construction via SetLifecycleHook to avoid bloating NewService
	// and to dodge a construction-time cycle with hostswriter.
	hook LifecycleHook

	// metricsCache holds the latest collected metrics per VM.
	// Written by collectAndPersistMetrics, read by SSE handlers via GetCachedMetrics.
	metricsMu    sync.RWMutex
	metricsCache map[string]vmdomain.Metrics

	// fleetBroadcaster fans out VM snapshots to SSE subscribers. Started once
	// via StartFleetBroadcaster; nil-safe if never started.
	fleetBroadcaster *fleetbroadcaster.Broadcaster

	// activityBroadcaster fans out recent runtime events to SSE subscribers.
	// Started once via StartActivityBroadcaster; nil-safe if never started.
	activityBroadcaster *activitybroadcaster.Broadcaster
}

// LifecycleHook is notified after VM lifecycle transitions. Implementations
// must be safe to call inline from service goroutines; they should return
// quickly or offload work themselves.
type LifecycleHook interface {
	OnVMChanged(ctx context.Context, vm *vmdomain.VM)
}

// SetLifecycleHook installs a hook that gets notified after Create/Destroy.
// Optional — production wires hostswriter.Hook; tests omit it.
func (s *Service) SetLifecycleHook(h LifecycleHook) { s.hook = h }

// notifyHook fires the hook if one is configured. Safe to call from any path.
func (s *Service) notifyHook(ctx context.Context, vm *vmdomain.VM) {
	if s.hook == nil {
		return
	}
	s.hook.OnVMChanged(ctx, vm)
}

// resolveUniqueName ensures vm.Name is unique before persistence. For
// explicit user-provided names, it pre-checks via GetByName and returns 409 on
// collision. For generated names, it retries up to 5 times then falls back to
// a UUID-derived suffix.
func (s *Service) resolveUniqueName(ctx context.Context, vm *vmdomain.VM, explicit bool) error {
	logger := pkglog.FromContext(ctx)

	if explicit {
		existing, err := s.repo.GetByName(ctx, vm.Name)
		if err == nil && existing != nil {
			return exception.Conflict("vm name already in use")
		}
		return nil
	}

	for attempts := 0; attempts < nameCollisionRetries; attempts++ {
		existing, err := s.repo.GetByName(ctx, vm.Name)
		if err != nil || existing == nil {
			return nil
		}
		logger.DebugContext(ctx, "vm name collision; regenerating", "attempt", attempts+1, "vm_name", vm.Name)
		vm.Name = naming.Generate()
	}

	// Exhausted retries: derive a deterministic, unique name from the UUID.
	vm.Name = naming.GenerateWithSuffix(vm.ID)
	logger.InfoContext(ctx, "vm name set to suffixed fallback", "vm_name", vm.Name)
	return nil
}

// isUniqueViolation reports whether err is a Postgres unique_violation (23505).
func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505"
	}
	return false
}

const nameCollisionRetries = 5

// NetworkConfig carries network parameters from app config to the VM service.
type NetworkConfig struct {
	BridgeName string
	Subnet     string
	GatewayIP  string
	DNSIP      string
}

// SnapshotDiskConfig controls disk snapshot behavior (full/self_contained/incremental).
type SnapshotDiskConfig struct {
	DiskMode           string // "full" | "self_contained" | "incremental"
	MaxChainLength     int
	MaxChainAgeMinutes int
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
func NewService(repo vmdomain.Repository, metricsRepo vmdomain.MetricsHistoryRepository, backend vmdomain.Backend, envRepo EnvironmentRepository, snapRepo snapshot.Repository, snapMetricsRepo snapshot.MetricsRepository, snapshotStore ports.SnapshotStore, runtimeEventRepo orchdomain.RuntimeEventRepository, idleTimeout time.Duration, autoSnapshot bool, resumeGrace time.Duration, networkCfg NetworkConfig, ipAllocator *IPAllocator, snapDiskCfg SnapshotDiskConfig) *Service {
	if idleTimeout <= 0 {
		idleTimeout = 5 * time.Minute
	}
	if resumeGrace <= 0 {
		resumeGrace = 2 * time.Minute
	}
	if snapDiskCfg.DiskMode == "" {
		snapDiskCfg.DiskMode = "self_contained"
	}
	if snapDiskCfg.MaxChainLength <= 0 {
		snapDiskCfg.MaxChainLength = 5
	}
	if snapDiskCfg.MaxChainAgeMinutes <= 0 {
		snapDiskCfg.MaxChainAgeMinutes = 120
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
		snapDiskCfg:   snapDiskCfg,
		metricsCache:  make(map[string]vmdomain.Metrics),

		fleetBroadcaster:   fleetbroadcaster.New(repo, 2*time.Second),
		activityBroadcaster: activitybroadcaster.New(runtimeEventRepo, 2*time.Second, 100),
	}
}

// releaseIPOnTerminate frees the allocated IP both in the DB (via the
// allocator) and in the in-memory VM state. Call before s.repo.Update when
// terminating a VM, so the subsequent Update does not write the released IP
// back to the row and the next allocation can reuse it.
func (s *Service) releaseIPOnTerminate(ctx context.Context, vm *vmdomain.VM) {
	if vm.IPAddress == nil || s.ipAllocator == nil {
		return
	}
	logger := pkglog.FromContext(ctx)
	if err := s.ipAllocator.Release(ctx, vm.ID); err != nil {
		logger.WarnContext(ctx, "failed to release IP on terminate", "vm_id", vm.ID, "error", err)
	}
	vm.IPAddress = nil
}

// StartFleetBroadcaster launches the background goroutine that refreshes
// the fleet snapshot every 2s and fans it out to SSE subscribers. Blocks
// until ctx is cancelled; run in a goroutine.
func (s *Service) StartFleetBroadcaster(ctx context.Context) {
	if s.fleetBroadcaster == nil {
		return
	}
	s.fleetBroadcaster.Start(ctx)
}

// SubscribeFleet registers a new SSE subscriber and returns a receive
// channel plus an unsubscribe func. If a snapshot is already available
// it is delivered immediately.
func (s *Service) SubscribeFleet() (<-chan []*vmdomain.VM, func()) {
	if s.fleetBroadcaster == nil {
		ch := make(chan []*vmdomain.VM)
		return ch, func() {}
	}
	return s.fleetBroadcaster.Subscribe()
}

// StartActivityBroadcaster launches the background goroutine that polls
// recent runtime events every 2s and fans them out to SSE subscribers.
// Blocks until ctx is cancelled; run in a goroutine.
func (s *Service) StartActivityBroadcaster(ctx context.Context) {
	if s.activityBroadcaster == nil {
		return
	}
	s.activityBroadcaster.Start(ctx)
}

// SubscribeActivity registers a new SSE subscriber for runtime events and
// returns a receive channel plus an unsubscribe func. If a batch is
// already available it is delivered immediately (the lookback window).
func (s *Service) SubscribeActivity() (<-chan []*orchdomain.PersistedRuntimeEvent, func()) {
	if s.activityBroadcaster == nil {
		ch := make(chan []*orchdomain.PersistedRuntimeEvent)
		return ch, func() {}
	}
	return s.activityBroadcaster.Subscribe()
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

// ResolveEnvironmentHint returns the environment type name (e.g. "uv", "bun")
// for a VM. The short name is used both as the system-prompt active-environment
// hint and as the "environment" field in tool result payloads.
func (s *Service) ResolveEnvironmentHint(ctx context.Context, vmID string) (string, error) {
	vm, err := s.repo.GetByID(ctx, vmID)
	if err != nil {
		return "", err
	}
	env, err := s.envRepo.GetByID(ctx, vm.EnvironmentID)
	if err != nil {
		return "", err
	}
	return string(env.Type), nil
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
				s.releaseIPOnTerminate(ctx, vm)
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
			s.releaseIPOnTerminate(ctx, vm)
		}

		if err := s.repo.Update(ctx, vm); err != nil {
			logger.WarnContext(ctx, "VM runtime reconciler failed to persist runtime state", "vm_id", vm.ID, "error", err)
		}
	}
}

// StartSnapshotGC runs a periodic background job that deletes snapshots
// belonging to terminated VMs (orphan safety net).
func (s *Service) StartSnapshotGC(ctx context.Context, interval time.Duration) {
	logger := pkglog.FromContext(ctx)
	if s.snapRepo == nil {
		logger.InfoContext(ctx, "snapshot GC skipped: no snapshot repository configured")
		return
	}
	if interval <= 0 {
		interval = 24 * time.Hour
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.InfoContext(ctx, "snapshot GC started", "interval", interval.String())

	for {
		select {
		case <-ctx.Done():
			logger.InfoContext(ctx, "snapshot GC stopped")
			return
		case <-ticker.C:
			s.gcOrphanedSnapshots(ctx)
		}
	}
}

func (s *Service) gcOrphanedSnapshots(ctx context.Context) {
	logger := pkglog.FromContext(ctx)

	orphans, err := s.snapRepo.ListOrphaned(ctx, time.Hour)
	if err != nil {
		logger.WarnContext(ctx, "snapshot GC: failed to list orphaned snapshots", "error", err)
		return
	}

	if len(orphans) == 0 {
		return
	}

	logger.InfoContext(ctx, "snapshot GC: cleaning orphaned snapshots", "count", len(orphans))
	for _, snap := range orphans {
		if err := s.DeleteSnapshot(ctx, snap.ID); err != nil {
			logger.WarnContext(ctx, "snapshot GC: failed to delete orphaned snapshot",
				"snapshot_id", snap.ID, "vm_id", snap.VMID, "error", err)
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
// name is optional — empty string means "generate a random Docker-style name".
// A non-empty name is normalized and must be unique; explicit collisions
// return 409, generated collisions are retried with a UUID-derived suffix.
func (s *Service) Create(ctx context.Context, envID, conversationID string, provider vmdomain.Provider, name string, workspaceSizeGB int, vcpu, memoryMB, diskMB, servicePort *int) (*vmdomain.VM, error) {
	logger := pkglog.FromContext(ctx)

	logger.DebugContext(ctx, "VM create: starting", "env_id", envID, "conversation_id", conversationID, "provider", provider, "name", name, "workspace_size_gb", workspaceSizeGB)

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

	var namePtr *string
	if strings.TrimSpace(name) != "" {
		normalized := vmdomain.NormalizeName(name)
		if normalized == "" {
			return nil, exception.BadRequest("invalid vm name")
		}
		namePtr = &normalized
	}

	// Create VM entity with optional resource overrides
	vm := vmdomain.New(vmdomain.CreateParams{
		EnvironmentID:   envID,
		ConversationID:  strings.TrimSpace(conversationID),
		Provider:        provider,
		WorkspaceSizeGB: workspaceSizeGB,
		Name:            namePtr,
		VCPU:            vcpu,
		MemoryMB:        memoryMB,
		DiskMB:          diskMB,
		ServicePort:     servicePort,
	})
	vm.DiffSnapshotsEnabled = env.DiffSnapshots

	if err := s.resolveUniqueName(ctx, vm, namePtr != nil); err != nil {
		return nil, err
	}

	// Persist VM to database
	if err := s.repo.Create(ctx, vm); err != nil {
		if isUniqueViolation(err) {
			// Race: a concurrent Create with the same explicit name beat us,
			// or a generated name collided despite the pre-check. For
			// explicit names this is a 409. For generated names, fall back
			// to a UUID-derived suffix and retry once.
			if namePtr != nil {
				return nil, exception.Conflict("vm name already in use")
			}
			vm.Name = naming.GenerateWithSuffix(vm.ID)
			if err := s.repo.Create(ctx, vm); err != nil {
				logger.ErrorContext(ctx, "failed to persist VM after name fallback", "vm_id", vm.ID, "vm_name", vm.Name, "error", err)
				return nil, err
			}
		} else {
			logger.ErrorContext(ctx, "failed to persist VM", "env_id", envID, "error", err)
			return nil, err
		}
	}

	logger.DebugContext(ctx, "VM create: persisted to database", "vm_id", vm.ID, "vm_name", vm.Name)

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
		s.releaseIPOnTerminate(ctx, vm)
		_ = s.repo.Update(ctx, vm)
		return nil, exception.Internal(fmt.Errorf("prepare workspace image: %w", err))
	}

	backendID, err := s.backend.Create(ctx, spec)
	if err != nil {
		logger.ErrorContext(ctx, "backend provisioning failed", "vm_id", vm.ID, "error", err)
		// Mark VM as terminated on failure
		vm.Terminate()
		s.releaseIPOnTerminate(ctx, vm)
		s.repo.Update(ctx, vm)
		return nil, exception.Internal(err)
	}

	logger.DebugContext(ctx, "VM create: backend provisioned", "vm_id", vm.ID, "backend_id", backendID, "vcpu", effectiveVCPU, "memory_mb", effectiveMemory, "disk_mb", effectiveDisk)

	if err := s.ensureWorkspaceMounted(ctx, backendID); err != nil {
		logger.ErrorContext(ctx, "workspace mount failed after create", "vm_id", vm.ID, "error", err)
		_ = s.backend.Destroy(ctx, backendID)
		vm.Terminate()
		s.releaseIPOnTerminate(ctx, vm)
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

	logger.InfoContext(ctx, "VM provisioned", "vm_id", vm.ID, "vm_name", vm.Name, "backend_id", backendID, "provider", provider)
	s.notifyHook(ctx, vm)
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
		vms, err = s.repo.List(ctx)
	} else {
		vms, err = s.repo.GetAllByUserID(ctx, userID)
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

	logger.InfoContext(ctx, "VM assigned to chat", "vm_id", vmID, "vm_name", vm.Name, "chat_id", chatID)
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

	logger.InfoContext(ctx, "VM stopped", "vm_id", id, "vm_name", vm.Name)
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
		logger.ErrorContext(ctx, "failed to delete VM", "vm_id", id, "vm_name", vm.Name, "error", err)
		return err
	}

	logger.InfoContext(ctx, "VM destroyed", "vm_id", id, "vm_name", vm.Name)
	s.notifyHook(ctx, vm)
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

// CreateSnapshot creates a snapshot of the running VM.
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

	// Determine snapshot type: incremental if any previous snapshot exists and diff snapshots enabled.
	snapType := snapshot.TypeFull
	var parentID *string
	diffEnabled := vm.DiffSnapshotsEnabled

	if diffEnabled {
		previousSnap, prevErr := s.snapRepo.GetLatestByVMID(ctx, vmID)
		if prevErr == nil && previousSnap != nil {
			snapType = snapshot.TypeIncremental
			parentID = &previousSnap.ID
			logger.InfoContext(ctx, "incremental snapshot: found previous snapshot",
				"vm_id", vmID, "parent_snapshot_id", previousSnap.ID)
		} else {
			logger.InfoContext(ctx, "no existing snapshot, creating initial full snapshot", "vm_id", vmID)
		}
	}

	// Decide disk snapshot strategy based on config mode.
	fullDisk := s.shouldCreateFullDisk(ctx, vmID)
	diskType := s.snapDiskCfg.DiskMode
	if s.snapDiskCfg.DiskMode == "incremental" && fullDisk {
		diskType = "full" // compaction triggered
	}

	// Build metadata capturing the VM spec at snapshot time.
	guestCID := uint32(0)
	if vm.GuestCID != nil {
		guestCID = *vm.GuestCID
	}
	guestPort := uint32(10789) // default guest agent port
	meta := snapshot.SnapshotMetadata{
		EnvironmentID:   vm.EnvironmentID,
		ImagePath:       env.ImagePath,
		VCPU:            vm.GetVCPU(env.GetVCPU()),
		MemoryMB:        vm.GetMemoryMB(env.GetMemoryMB()),
		DiskMB:          vm.GetDiskMB(env.GetDiskMB()),
		GuestCID:        guestCID,
		GuestPort:       guestPort,
		RootfsPath:      filepath.Join("/var/lib/firecracker/vms", vm.ID, "rootfs.ext4"),
		DiffSnapshots:   diffEnabled,
		BaseImagePath:   env.ImagePath,
		DiskSnapshotType: diskType,
		DiskChainLength:  s.resolveChainLength(ctx, vmID, diskType),
	}
	if parentID != nil {
		// BaseSnapshotID tracks the chain root (full snapshot) for memory merge.
		// Walk up to find it, or use the parent if it's a full.
		if baseFull, fullErr := s.snapRepo.GetLatestFull(ctx, vmID); fullErr == nil && baseFull != nil {
			meta.BaseSnapshotID = baseFull.ID
		} else {
			meta.BaseSnapshotID = *parentID
		}
	}
	metaJSON, _ := json.Marshal(meta)

	// Call backend to create the snapshot with the appropriate disk strategy.
	snapResult, err := s.backend.CreateSnapshot(ctx, vmID, vmdomain.SnapshotOptions{FullDisk: fullDisk})
	if err != nil {
		logger.ErrorContext(ctx, "backend snapshot failed", "vm_id", vmID, "error", err)
		return nil, exception.Internal(err)
	}

	snapDir := snapResult.SnapshotDir
	sizeBytes := snapResult.MemoryBytes + snapResult.CowBytes + snapResult.DiskBytes

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

		// Choose which disk file to upload based on disk type.
		snapshotFiles := []string{"memory", "state"}
		switch diskType {
		case "full", "self_contained", "":
			snapshotFiles = append(snapshotFiles, "disk")
		case "incremental":
			snapshotFiles = append(snapshotFiles, "cow")
		}

		var files []ports.SnapshotFile
		for _, name := range snapshotFiles {
			localPath := filepath.Join(snapDir, name)
			if _, err := os.Stat(localPath); err != nil {
				continue
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
			logger.InfoContext(ctx, "snapshot uploaded to storage", "snapshot_id", snap.ID, "s3_prefix", s3Prefix, "bytes", uploaded, "disk_type", diskType)
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
		DiskBytes:       snapResult.DiskBytes,
		CreatedAt:       time.Now().UTC(),
	}
	if err := s.snapMetricsRepo.Insert(ctx, metrics); err != nil {
		logger.WarnContext(ctx, "failed to persist snapshot metrics", "vm_id", vmID, "error", err)
	}

	logger.InfoContext(ctx, "VM snapshot created", "vm_id", vmID, "snapshot_id", snap.ID, "size_bytes", sizeBytes, "disk_type", diskType)

	// Chain-aware GC.
	go func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.snapshotGC(cleanupCtx, vmID, snap.ID)
	}()

	return snap, nil
}

// shouldCreateFullDisk decides whether to flatten the full disk or just copy the cow delta.
func (s *Service) shouldCreateFullDisk(ctx context.Context, vmID string) bool {
	switch s.snapDiskCfg.DiskMode {
	case "full", "self_contained", "":
		return true
	case "incremental":
		// Check chain policy.
		previous, err := s.snapRepo.GetLatestByVMID(ctx, vmID)
		if err != nil || previous == nil {
			return true // no previous snapshot → first → full
		}
		meta, err := previous.ParseMetadata()
		if err != nil || meta == nil {
			return true
		}
		if meta.DiskChainLength+1 >= s.snapDiskCfg.MaxChainLength {
			return true // next snapshot would exceed max chain depth
		}
		age := time.Since(previous.CreatedAt)
		if age > time.Duration(s.snapDiskCfg.MaxChainAgeMinutes)*time.Minute {
			return true // chain too old → compact
		}
		return false
	default:
		return true
	}
}

// resolveChainLength returns the disk chain length for the new snapshot.
func (s *Service) resolveChainLength(ctx context.Context, vmID string, diskType string) int {
	if diskType != "incremental" {
		return 0 // full or self_contained → new chain root
	}
	previous, err := s.snapRepo.GetLatestByVMID(ctx, vmID)
	if err != nil || previous == nil {
		return 0
	}
	meta, _ := previous.ParseMetadata()
	if meta == nil {
		return 0
	}
	return meta.DiskChainLength + 1
}

// snapshotGC performs chain-aware garbage collection for a VM's snapshots.
// It keeps the latest snapshot and all its ancestors, deleting everything else.
func (s *Service) snapshotGC(ctx context.Context, vmID string, latestID string) {
	logger := pkglog.FromContext(ctx)

	snaps, err := s.snapRepo.GetByVMID(ctx, vmID)
	if err != nil || len(snaps) <= 1 {
		return
	}

	// Find the latest snapshot.
	var latest *snapshot.Snapshot
	for _, snap := range snaps {
		if snap.ID == latestID {
			latest = snap
			break
		}
	}
	if latest == nil {
		return
	}

	// Collect all ancestors of the latest (protected — chain dependencies).
	protected := s.collectAncestors(ctx, latest, snaps)
	protected[latest.ID] = true

	for _, snap := range snaps {
		if protected[snap.ID] {
			continue
		}
		if err := s.DeleteSnapshot(ctx, snap.ID); err != nil {
			logger.WarnContext(ctx, "snapshot GC: failed to delete snapshot",
				"snapshot_id", snap.ID, "error", err)
		}
	}
}

// collectAncestors walks the parent chain from leaf to root, returning
// the set of snapshot IDs that must be preserved.
func (s *Service) collectAncestors(ctx context.Context, leaf *snapshot.Snapshot, all []*snapshot.Snapshot) map[string]bool {
	byID := make(map[string]*snapshot.Snapshot, len(all))
	for _, snap := range all {
		byID[snap.ID] = snap
	}

	ancestors := make(map[string]bool)
	current := leaf
	for current != nil && current.ParentSnapshotID != nil {
		parent, ok := byID[*current.ParentSnapshotID]
		if !ok {
			break
		}
		ancestors[parent.ID] = true
		current = parent
	}
	return ancestors
}

// resolveCowChain walks the parent chain from leaf to the true root
// (type=full snapshot with complete memory), returning the ordered list
// from root to leaf. Stops at type=full, NOT disk_snapshot_type=full,
// because compaction snapshots can have full disk but diff memory.
func (s *Service) resolveCowChain(ctx context.Context, leaf *snapshot.Snapshot) ([]*snapshot.Snapshot, error) {
	var chain []*snapshot.Snapshot
	current := leaf
	for current != nil {
		chain = append(chain, current)
		if current.ParentSnapshotID == nil {
			break
		}
		parent, err := s.snapRepo.GetByID(ctx, *current.ParentSnapshotID)
		if err != nil {
			return nil, fmt.Errorf("get parent snapshot %s: %w", *current.ParentSnapshotID, err)
		}
		// Stop at a true full snapshot (has complete memory dump).
		if parent.Type == snapshot.TypeFull {
			chain = append(chain, parent)
			break
		}
		current = parent
	}
	// Reverse: root first, leaf last.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain, nil
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

	// If the latest snapshot is incremental, resolve the chain for memory merge.
	// The chain is also used for incremental disk reconstruction when applicable.
	var baseSnap *snapshot.Snapshot
	var diskChain []*snapshot.Snapshot
	if snap.IsIncremental() && snap.ParentSnapshotID != nil {
		// Always resolve the full chain: memory merge needs all intermediate
		// diffs even when the disk is self-contained (compaction case).
		diskChain, err = s.resolveCowChain(ctx, snap)
		if err != nil || len(diskChain) == 0 {
			logger.WarnContext(ctx, "failed to resolve cow chain, falling back to new VM",
				"vm_id", vmID, "error", err)
			return s.Create(ctx, meta.EnvironmentID, chatID, vmdomain.ProviderFirecracker, "",
				vm.WorkspaceSizeGB, nil, nil, nil, nil)
		}
		baseSnap = diskChain[0]
		logger.InfoContext(ctx, "resolved snapshot chain for restore",
			"vm_id", vmID, "chain_depth", len(diskChain),
			"root_snapshot_id", baseSnap.ID, "disk_type", meta.DiskSnapshotType)
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

		// Check for new-format full disk image first, fall back to CoW delta.
		// The new format (disk.zst) is self-contained — no chain reconstruction needed.
		var hasDiskImage bool
		if meta.DiffSnapshots {
			if meta.DiskSnapshotType == "incremental" && len(diskChain) > 0 {
				// Incremental disk mode: download cow delta from this snapshot.
				// The root disk and chain cows are downloaded separately below.
				entries = append(entries, dlEntry{name: "cow"})
			} else {
				diskZstKey := snap.SnapshotPath + "/disk.zst"
				diskRawKey := snap.SnapshotPath + "/disk"
				if exists, _ := s.snapshotStore.ObjectExists(ctx, diskZstKey); exists {
					entries = append(entries, dlEntry{name: "disk"})
					hasDiskImage = true
				} else if exists, _ := s.snapshotStore.ObjectExists(ctx, diskRawKey); exists {
					entries = append(entries, dlEntry{name: "disk"})
					hasDiskImage = true
				} else {
					// Legacy format: CoW delta only.
					entries = append(entries, dlEntry{name: "cow"})
				}
			}
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

		// Decompress .zst files after download.
		// Disk and cow go to VM dir, not tmpDir.
		vmDir := filepath.Join("/var/lib/firecracker/vms", vmID)
		for _, e := range entries {
			if e.compressed {
				zstPath := filepath.Join(tmpDir, e.name+".zst")
				rawPath := filepath.Join(tmpDir, e.name)
				if e.name == "cow" {
					rawPath = filepath.Join(vmDir, "cow.img")
				} else if e.name == "disk" {
					rawPath = filepath.Join(vmDir, "disk.img")
				}
				if err := fcutil.DecompressFileZstd(zstPath, rawPath); err != nil {
					os.RemoveAll(tmpDir)
					return nil, exception.Internal(fmt.Errorf("failed to decompress %s: %w", e.name, err))
				}
				_ = os.Remove(zstPath)
			} else if e.name == "cow" {
				src := filepath.Join(tmpDir, "cow")
				dst := filepath.Join(vmDir, "cow.img")
				if err := os.Rename(src, dst); err != nil {
					return nil, exception.Internal(fmt.Errorf("failed to move cow to vm dir: %w", err))
				}
			} else if e.name == "disk" {
				src := filepath.Join(tmpDir, "disk")
				dst := filepath.Join(vmDir, "disk.img")
				if err := os.Rename(src, dst); err != nil {
					return nil, exception.Internal(fmt.Errorf("failed to move disk image to vm dir: %w", err))
				}
			}
		}

		// Handle disk image vs CoW chain for restore.
		// Disk and memory are independent: disk can use the self-contained
		// disk.img while memory still needs incremental merging.
		if hasDiskImage {
			// Self-contained full disk image.
			spec.DiskImagePath = filepath.Join(vmDir, "disk.img")
			spec.Runtime.EnableDiffSnapshots = true
			logger.InfoContext(ctx, "using full disk image for restore (no chain reconstruction)",
				"vm_id", vmID, "disk_image", spec.DiskImagePath)
			} else if meta.DiskSnapshotType == "incremental" && len(diskChain) > 0 {
				// Incremental disk: find the most recent full-disk snapshot in the chain
				// (compaction point) and use its disk as root. Download cow deltas only
				// for chain members after that point.
				diskRootIdx := 0
				for i, chainSnap := range diskChain {
					cm, _ := chainSnap.ParseMetadata()
					if cm != nil && (cm.DiskSnapshotType == "full" || cm.DiskSnapshotType == "self_contained") {
						diskRootIdx = i
					}
				}
				diskRoot := diskChain[diskRootIdx]

				// Download the disk root's full disk image.
				rootDiskZst := diskRoot.SnapshotPath + "/disk.zst"
				rootDiskRaw := diskRoot.SnapshotPath + "/disk"
				diskPath := filepath.Join(vmDir, "disk.img")
				if exists, _ := s.snapshotStore.ObjectExists(ctx, rootDiskZst); exists {
					if err := s.snapshotStore.DownloadFiles(ctx, []ports.SnapshotFile{
						{Key: rootDiskZst, LocalPath: filepath.Join(tmpDir, "root_disk.zst")},
					}); err != nil {
						return nil, exception.Internal(fmt.Errorf("download root disk: %w", err))
					}
					if err := fcutil.DecompressFileZstd(filepath.Join(tmpDir, "root_disk.zst"), diskPath); err != nil {
						return nil, exception.Internal(fmt.Errorf("decompress root disk: %w", err))
					}
					_ = os.Remove(filepath.Join(tmpDir, "root_disk.zst"))
				} else {
					if err := s.snapshotStore.DownloadFiles(ctx, []ports.SnapshotFile{
						{Key: rootDiskRaw, LocalPath: diskPath},
					}); err != nil {
						return nil, exception.Internal(fmt.Errorf("download root disk (raw): %w", err))
					}
				}
				spec.DiskImagePath = diskPath
				spec.Runtime.EnableDiffSnapshots = true

				// Download cow deltas from chain members after the disk root.
				var cowPaths []string
				for i, chainSnap := range diskChain[diskRootIdx+1:] {
					cowZst := chainSnap.SnapshotPath + "/cow.zst"
					cowRaw := chainSnap.SnapshotPath + "/cow"
					cowLocal := filepath.Join(vmDir, fmt.Sprintf("cow_chain_%d.img", i))

					if exists, _ := s.snapshotStore.ObjectExists(ctx, cowZst); exists {
						if err := s.snapshotStore.DownloadFiles(ctx, []ports.SnapshotFile{
							{Key: cowZst, LocalPath: filepath.Join(tmpDir, fmt.Sprintf("chain_cow_%d.zst", i))},
						}); err != nil {
							return nil, exception.Internal(fmt.Errorf("download chain cow %d: %w", i, err))
						}
						if err := fcutil.DecompressFileZstd(
							filepath.Join(tmpDir, fmt.Sprintf("chain_cow_%d.zst", i)),
							cowLocal,
						); err != nil {
							return nil, exception.Internal(fmt.Errorf("decompress chain cow %d: %w", i, err))
						}
						_ = os.Remove(filepath.Join(tmpDir, fmt.Sprintf("chain_cow_%d.zst", i)))
					} else {
						if err := s.snapshotStore.DownloadFiles(ctx, []ports.SnapshotFile{
							{Key: cowRaw, LocalPath: cowLocal},
						}); err != nil {
							return nil, exception.Internal(fmt.Errorf("download chain cow %d (raw): %w", i, err))
						}
					}
					cowPaths = append(cowPaths, cowLocal)
				}

				if len(cowPaths) > 0 {
					spec.CowChainPaths = cowPaths
				}
				logger.InfoContext(ctx, "incremental disk chain prepared for restore",
					"vm_id", vmID, "root_disk", diskPath,
					"disk_root_idx", diskRootIdx, "cow_chain", len(cowPaths))
			}

		// Incremental memory merge: download base memory and apply diffs.
		if snap.IsIncremental() && baseSnap != nil {
			if len(diskChain) > 1 {
				// Multi-step merge: chain has depth > 1, merge all intermediate diffs.
				baseMemoryDir := filepath.Join(tmpDir, "chain_mem")
				if err := os.MkdirAll(baseMemoryDir, 0755); err != nil {
					return nil, exception.Internal(fmt.Errorf("create chain memory dir: %w", err))
				}

				// Download root (full) memory as the base.
				root := diskChain[0]
				rootMemZst := root.SnapshotPath + "/memory.zst"
				rootMemRaw := root.SnapshotPath + "/memory"
				baseMemLocal := filepath.Join(baseMemoryDir, "base_memory")
				baseDownloaded := false
				if exists, _ := s.snapshotStore.ObjectExists(ctx, rootMemZst); exists {
					if err := s.snapshotStore.DownloadFiles(ctx, []ports.SnapshotFile{
						{Key: rootMemZst, LocalPath: baseMemLocal + ".zst"},
					}); err != nil {
						logger.WarnContext(ctx, "download root memory.zst failed", "vm_id", vmID, "error", err)
					} else if err := fcutil.DecompressFileZstd(baseMemLocal+".zst", baseMemLocal); err != nil {
						logger.WarnContext(ctx, "decompress root memory failed", "vm_id", vmID, "error", err)
					} else {
						_ = os.Remove(baseMemLocal + ".zst")
						baseDownloaded = true
					}
				}
				if !baseDownloaded {
					if err := s.snapshotStore.DownloadFiles(ctx, []ports.SnapshotFile{
						{Key: rootMemRaw, LocalPath: baseMemLocal},
					}); err != nil {
						logger.WarnContext(ctx, "root memory missing; falling back to new VM",
							"vm_id", vmID, "error", err)
						return s.Create(ctx, meta.EnvironmentID, chatID, vmdomain.ProviderFirecracker, "",
							vm.WorkspaceSizeGB, nil, nil, nil, nil)
					}
				}

				// Iteratively merge each chain member's diff memory onto the accumulated base.
				accumMemPath := baseMemLocal
				for ci, chainSnap := range diskChain[1:] {
					diffMemZst := chainSnap.SnapshotPath + "/memory.zst"
					diffMemRaw := chainSnap.SnapshotPath + "/memory"
					diffMemLocal := filepath.Join(baseMemoryDir, fmt.Sprintf("diff_%d", ci))
					diffDownloaded := false
					if exists, _ := s.snapshotStore.ObjectExists(ctx, diffMemZst); exists {
						if err := s.snapshotStore.DownloadFiles(ctx, []ports.SnapshotFile{
							{Key: diffMemZst, LocalPath: diffMemLocal + ".zst"},
						}); err != nil {
							logger.WarnContext(ctx, "download diff memory failed", "chain_idx", ci, "error", err)
						} else if err := fcutil.DecompressFileZstd(diffMemLocal+".zst", diffMemLocal); err != nil {
							logger.WarnContext(ctx, "decompress diff memory failed", "chain_idx", ci, "error", err)
						} else {
							_ = os.Remove(diffMemLocal + ".zst")
							diffDownloaded = true
						}
					}
					if !diffDownloaded {
						if err := s.snapshotStore.DownloadFiles(ctx, []ports.SnapshotFile{
							{Key: diffMemRaw, LocalPath: diffMemLocal},
						}); err != nil {
							logger.WarnContext(ctx, "diff memory missing; falling back", "chain_idx", ci, "error", err)
							return s.Create(ctx, meta.EnvironmentID, chatID, vmdomain.ProviderFirecracker, "",
								vm.WorkspaceSizeGB, nil, nil, nil, nil)
						}
					}

					// Download manifest for this diff.
					manKey := chainSnap.SnapshotPath + "/memory.manifest"
					manLocal := filepath.Join(baseMemoryDir, fmt.Sprintf("manifest_%d", ci))
					if err := s.snapshotStore.DownloadFiles(ctx, []ports.SnapshotFile{
						{Key: manKey, LocalPath: manLocal},
					}); err != nil {
						logger.WarnContext(ctx, "manifest download failed; falling back", "chain_idx", ci, "error", err)
						return s.Create(ctx, meta.EnvironmentID, chatID, vmdomain.ProviderFirecracker, "",
							vm.WorkspaceSizeGB, nil, nil, nil, nil)
					}
					manifest, manErr := fcutil.ReadManifest(manLocal)
					if manErr != nil {
						logger.WarnContext(ctx, "manifest parse failed; falling back", "chain_idx", ci, "error", manErr)
						return s.Create(ctx, meta.EnvironmentID, chatID, vmdomain.ProviderFirecracker, "",
							vm.WorkspaceSizeGB, nil, nil, nil, nil)
					}

					mergedPath := filepath.Join(baseMemoryDir, fmt.Sprintf("merged_%d", ci))
					logger.InfoContext(ctx, "merging chain memory diff",
						"vm_id", vmID, "chain_idx", ci, "regions", len(manifest))
					if mergeErr := fcutil.MergeDiffMemory(accumMemPath, diffMemLocal, mergedPath, manifest); mergeErr != nil {
						logger.WarnContext(ctx, "memory merge failed; falling back", "chain_idx", ci, "error", mergeErr)
						return s.Create(ctx, meta.EnvironmentID, chatID, vmdomain.ProviderFirecracker, "",
							vm.WorkspaceSizeGB, nil, nil, nil, nil)
					}
					accumMemPath = mergedPath
				}

				// Copy the fully merged memory to the restore dir.
				diffMemoryPath := filepath.Join(tmpDir, "memory")
				if err := os.Rename(accumMemPath, diffMemoryPath); err != nil {
					return nil, exception.Internal(fmt.Errorf("rename merged memory: %w", err))
				}
				spec.RestoreAsFull = true
				logger.InfoContext(ctx, "multi-step memory merge complete",
					"vm_id", vmID, "chain_steps", len(diskChain)-1)
			} else {
				// Single-step merge (depth 1): merge leaf diff onto base full.
				baseMemoryDir := filepath.Join(tmpDir, "base")
				if err := os.MkdirAll(baseMemoryDir, 0755); err != nil {
					return nil, exception.Internal(fmt.Errorf("create base snapshot dir: %w", err))
				}
				baseZstKey := baseSnap.SnapshotPath + "/memory.zst"
				baseRawKey := baseSnap.SnapshotPath + "/memory"
				baseZstLocal := filepath.Join(baseMemoryDir, "memory.zst")
				baseRawLocal := filepath.Join(baseMemoryDir, "memory")
				baseDownloaded := false
				if exists, _ := s.snapshotStore.ObjectExists(ctx, baseZstKey); exists {
					if err := s.snapshotStore.DownloadFiles(ctx, []ports.SnapshotFile{
						{Key: baseZstKey, LocalPath: baseZstLocal},
					}); err != nil {
						logger.WarnContext(ctx, "download base memory.zst failed", "vm_id", vmID, "error", err)
					} else if err := fcutil.DecompressFileZstd(baseZstLocal, baseRawLocal); err != nil {
						logger.WarnContext(ctx, "decompress base memory failed", "vm_id", vmID, "error", err)
					} else {
						_ = os.Remove(baseZstLocal)
						baseDownloaded = true
					}
				}
				if !baseDownloaded {
					if err := s.snapshotStore.DownloadFiles(ctx, []ports.SnapshotFile{
						{Key: baseRawKey, LocalPath: baseRawLocal},
					}); err != nil {
						logger.WarnContext(ctx, "base memory missing; falling back to new VM",
							"vm_id", vmID, "error", err)
						return s.Create(ctx, meta.EnvironmentID, chatID, vmdomain.ProviderFirecracker, "",
							vm.WorkspaceSizeGB, nil, nil, nil, nil)
					}
				}
				manifestPath := filepath.Join(tmpDir, "memory.manifest")
				manifest, manifestErr := fcutil.ReadManifest(manifestPath)
				if manifestErr != nil {
					logger.WarnContext(ctx, "failed to read memory manifest; falling back to new VM",
						"error", manifestErr)
					return s.Create(ctx, meta.EnvironmentID, chatID, vmdomain.ProviderFirecracker, "",
						vm.WorkspaceSizeGB, nil, nil, nil, nil)
				}
				diffMemoryPath := filepath.Join(tmpDir, "memory")
				mergedPath := filepath.Join(tmpDir, "memory.merged")
				logger.InfoContext(ctx, "merging incremental memory onto base",
					"vm_id", vmID, "regions", len(manifest))
				if mergeErr := fcutil.MergeDiffMemory(baseRawLocal, diffMemoryPath, mergedPath, manifest); mergeErr != nil {
					logger.WarnContext(ctx, "failed to merge diff memory; falling back to new VM",
						"error", mergeErr)
					return s.Create(ctx, meta.EnvironmentID, chatID, vmdomain.ProviderFirecracker, "",
						vm.WorkspaceSizeGB, nil, nil, nil, nil)
				}
				if renameErr := os.Rename(mergedPath, diffMemoryPath); renameErr != nil {
					return nil, exception.Internal(fmt.Errorf("rename merged memory: %w", renameErr))
				}

				// Legacy cow chain reconstruction for non-incremental disk mode.
				if !hasDiskImage && meta.DiskSnapshotType != "incremental" {
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
				spec.RestoreAsFull = true
				logger.InfoContext(ctx, "memory chain merge complete", "vm_id", vmID)
			}
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

	// GC: after resume the VM is self-contained (flattened), safe to delete all.
	go func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.deleteAllSnapshots(cleanupCtx, vmID); err != nil {
			logger.WarnContext(cleanupCtx, "snapshot GC after resume failed",
				"vm_id", vmID, "error", err)
		}
	}()

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

	// Re-read the list each iteration so we notice snapshots created
	// concurrently (e.g. auto-snapshot on stop) that would make a
	// parent undeletable.  DeleteSnapshot checks for children before
	// removing S3 files, so a parent with new children is kept intact.
	for {
		snaps, err := s.snapRepo.GetByVMID(ctx, vmID)
		if err != nil {
			return err
		}
		if len(snaps) == 0 {
			return nil
		}

		deleted := false
		for i := len(snaps) - 1; i >= 0; i-- {
			if derr := s.DeleteSnapshot(ctx, snaps[i].ID); derr != nil {
				continue // has children or other conflict — skip
			}
			deleted = true
		}
		if !deleted {
			// Every remaining snapshot has children; nothing more to do.
			return nil
		}
	}
}

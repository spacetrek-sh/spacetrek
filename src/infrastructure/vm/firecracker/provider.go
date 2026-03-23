// Package firecracker provides the Firecracker VM provider implementation.
package firecracker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	fcsdk "github.com/firecracker-microvm/firecracker-go-sdk"
	models "github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	vmdomain "github.com/kumori-sh/spacetrk/src/core/domain/vm"
)

// Provider implements the vmdomain.Backend interface for Firecracker.
type Provider struct {
	config Config
	mu     sync.RWMutex
	vms    map[string]*VMInstance // Track running VMs
}

// VMInstance represents a running Firecracker VM.
type VMInstance struct {
	ID         string
	SocketPath string
	Machine    *fcsdk.Machine
	Cancel     context.CancelFunc
	Config     vmdomain.CreateSpec
	StartedAt  time.Time
}

// NewProvider creates a new Firecracker provider.
func NewProvider(cfg Config) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Ensure base directory exists
	if err := os.MkdirAll(cfg.BaseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %w", err)
	}

	return &Provider{
		config: cfg,
		vms:    make(map[string]*VMInstance),
	}, nil
}

// Create creates and starts a new Firecracker VM.
func (p *Provider) Create(ctx context.Context, spec vmdomain.CreateSpec) (string, error) {
	logger := pkglog.FromContext(ctx)

	vmID := spec.EnvironmentID // Use environment ID as VM ID for now
	vmDir := p.config.VMDir(vmID)
	socketPath := p.config.SocketPath(vmID)

	if vmID == "" {
		return "", fmt.Errorf("environment ID is required")
	}

	// Create VM directory
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create VM directory: %w", err)
	}
	_ = os.Remove(socketPath)

	fcCfg := fcsdk.Config{
		SocketPath:      socketPath,
		KernelImagePath: p.config.KernelPath,
		KernelArgs:      p.config.KernelArgs,
		Drives:          fcsdk.NewDrivesBuilder(spec.ImagePath).Build(),
		MachineCfg: models.MachineConfiguration{
			VcpuCount:  fcsdk.Int64(int64(spec.Resources.VCPU)),
			MemSizeMib: fcsdk.Int64(int64(spec.Resources.MemoryMB)),
			Smt:        fcsdk.Bool(p.config.SMT),
		},
		VMID: vmID,
	}

	if spec.Runtime.Network.Enabled && spec.Runtime.Network.Interface != "" {
		fcCfg.NetworkInterfaces = []fcsdk.NetworkInterface{
			{
				StaticConfiguration: &fcsdk.StaticNetworkConfiguration{
					MacAddress:  p.config.MacAddress,
					HostDevName: spec.Runtime.Network.Interface,
				},
				AllowMMDS: p.config.EnableMmds,
			},
		}
	}

	cmd := fcsdk.VMCommandBuilder{}.
		WithBin(p.config.BinaryPath).
		WithSocketPath(socketPath).
		Build(ctx)

	machine, err := fcsdk.NewMachine(ctx, fcCfg, fcsdk.WithProcessRunner(cmd))
	if err != nil {
		_ = os.RemoveAll(vmDir)
		return "", fmt.Errorf("failed to create machine: %w", err)
	}

	machineCtx, cancel := context.WithCancel(context.Background())
	if err := machine.Start(machineCtx); err != nil {
		cancel()
		_ = os.RemoveAll(vmDir)
		return "", fmt.Errorf("failed to start firecracker: %w", err)
	}

	// Track the VM
	p.mu.Lock()
	p.vms[vmID] = &VMInstance{
		ID:         vmID,
		SocketPath: socketPath,
		Machine:    machine,
		Cancel:     cancel,
		Config:     spec,
		StartedAt:  time.Now(),
	}
	p.mu.Unlock()

	pid, _ := machine.PID()
	logger.Info("Firecracker VM created", "vm_id", vmID, "pid", pid, "socket", socketPath)
	return vmID, nil
}

// Start resumes a paused VM. Firecracker cannot restart a fully stopped VM.
func (p *Provider) Start(ctx context.Context, id string) error {
	p.mu.RLock()
	vm, exists := p.vms[id]
	p.mu.RUnlock()

	if !exists {
		return fmt.Errorf("VM not found: %s", id)
	}

	info, err := vm.Machine.DescribeInstanceInfo(ctx)
	if err != nil {
		return fmt.Errorf("failed to inspect VM state: %w", err)
	}

	state := stringValue(info.State)
	switch state {
	case models.InstanceInfoStatePaused:
		return vm.Machine.ResumeVM(ctx)
	case models.InstanceInfoStateRunning:
		return nil
	default:
		return fmt.Errorf("resume not supported for VM state %q", state)
	}
}

// Stop gracefully shuts down a VM.
func (p *Provider) Stop(ctx context.Context, id string) error {
	logger := pkglog.FromContext(ctx)

	p.mu.RLock()
	vm, exists := p.vms[id]
	p.mu.RUnlock()

	if !exists {
		return fmt.Errorf("VM not found: %s", id)
	}

	// Send Ctrl+Alt+Del to shutdown gracefully.
	if err := vm.Machine.Shutdown(ctx); err != nil {
		logger.Warn("Failed to send shutdown signal", "vm_id", id, "error", err)
		// Force cleanup if graceful shutdown fails
		return p.cleanup(ctx, id, true)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), time.Duration(p.config.ShutdownTimeout)*time.Second)
	defer cancel()
	if err := vm.Machine.Wait(waitCtx); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		logger.Warn("VM did not stop cleanly", "vm_id", id, "error", err)
		return p.cleanup(ctx, id, true)
	}

	// Remove from tracking
	p.mu.Lock()
	delete(p.vms, id)
	p.mu.Unlock()
	vm.Cancel()

	logger.Info("Firecracker VM stopped", "vm_id", id)
	return nil
}

// Destroy forcefully terminates and removes a VM.
func (p *Provider) Destroy(ctx context.Context, id string) error {
	p.mu.RLock()
	vm, exists := p.vms[id]
	p.mu.RUnlock()

	if !exists {
		return fmt.Errorf("VM not found: %s", id)
	}

	_ = vm
	return p.cleanup(ctx, id, true)
}

// Status returns the current runtime status of the VM.
func (p *Provider) Status(ctx context.Context, id string) (vmdomain.RuntimeStatus, error) {
	p.mu.RLock()
	vmInstance, exists := p.vms[id]
	p.mu.RUnlock()

	if !exists {
		return vmdomain.RuntimeStatus{}, fmt.Errorf("VM not found: %s", id)
	}

	pid, err := vmInstance.Machine.PID()
	if err != nil {
		pid = 0
	}

	uptime := 0
	if !vmInstance.StartedAt.IsZero() {
		uptime = int(time.Since(vmInstance.StartedAt).Seconds())
	}

	if !p.isProcessRunning(pid) {
		return vmdomain.RuntimeStatus{
			ID:        id,
			State:     "stopped",
			PID:       0,
			VCPU:      vmInstance.Config.Resources.VCPU,
			MemoryMB:  vmInstance.Config.Resources.MemoryMB,
			UptimeSec: uptime,
		}, nil
	}

	instanceInfo, err := vmInstance.Machine.DescribeInstanceInfo(ctx)
	if err != nil {
		return vmdomain.RuntimeStatus{}, fmt.Errorf("failed to read VM status: %w", err)
	}

	return vmdomain.RuntimeStatus{
		ID:        id,
		State:     p.mapState(stringValue(instanceInfo.State)),
		PID:       pid,
		VCPU:      vmInstance.Config.Resources.VCPU,
		MemoryMB:  vmInstance.Config.Resources.MemoryMB,
		UptimeSec: uptime,
	}, nil
}

// Execute runs a command inside the VM via vsock.
func (p *Provider) Execute(ctx context.Context, id string, cmd []string) (string, string, int, error) {
	p.mu.RLock()
	_, exists := p.vms[id]
	p.mu.RUnlock()

	if !exists {
		return "", "", -1, fmt.Errorf("VM not found: %s", id)
	}

	// For now, return a placeholder
	// In production, this would use vsock to communicate with an agent inside the VM
	return "", "", -1, fmt.Errorf("command execution not yet implemented")
}

// GetMetrics returns resource usage metrics for the VM.
func (p *Provider) GetMetrics(ctx context.Context, id string) (vmdomain.Metrics, error) {
	p.mu.RLock()
	vmInstance, exists := p.vms[id]
	p.mu.RUnlock()

	if !exists {
		return vmdomain.Metrics{}, fmt.Errorf("VM not found: %s", id)
	}

	return vmdomain.Metrics{
		CPUUsagePercent:      0,
		MemoryUsedMB:         0, // Firecracker doesn't expose memory usage
		MemoryLimitMB:        vmInstance.Config.Resources.MemoryMB,
		CollectedAt:          time.Now().Unix(),
		TasksExecuted:        0,
		TasksFailed:          0,
		DiskUsedMB:           0,
		DiskLimitMB:          0,
		DiskPercent:          0,
		MemoryPercent:        0,
		NetworkBytesSent:     0,
		NetworkBytesReceived: 0,
	}, nil
}

// cleanup removes a VM and cleans up resources.
func (p *Provider) cleanup(ctx context.Context, id string, removeDir bool) error {
	_ = ctx
	p.mu.Lock()
	vm, exists := p.vms[id]
	if exists {
		delete(p.vms, id)
	}
	p.mu.Unlock()

	if !exists {
		return nil
	}

	if vm.Cancel != nil {
		vm.Cancel()
	}

	if vm.Machine != nil {
		_ = vm.Machine.StopVMM()
		waitCtx, cancel := context.WithTimeout(context.Background(), time.Duration(p.config.ShutdownTimeout)*time.Second)
		defer cancel()
		_ = vm.Machine.Wait(waitCtx)
	}

	if removeDir {
		_ = os.RemoveAll(p.config.VMDir(id))
	}

	return nil
}

// isProcessRunning checks if a process is running.
func (p *Provider) isProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	err = proc.Signal(syscall.Signal(0))
	return err == nil || !strings.Contains(err.Error(), "process already finished")
}

func (p *Provider) mapState(raw string) string {
	switch raw {
	case models.InstanceInfoStateRunning:
		return "running"
	case models.InstanceInfoStatePaused:
		return "paused"
	case models.InstanceInfoStateNotStarted:
		return "stopped"
	default:
		return strings.ToLower(raw)
	}
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

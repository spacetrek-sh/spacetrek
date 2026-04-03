// Package firecracker provides the Firecracker VM provider implementation.
package firecracker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	prev   map[string]cpuSample
}

type cpuSample struct {
	procTicks  uint64
	totalTicks uint64
	time       time.Time
}

// VMInstance represents a running Firecracker VM.
type VMInstance struct {
	ID         string
	SocketPath string
	VsockPath  string
	GuestCID   uint32
	GuestPort  uint32
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
		prev:   make(map[string]cpuSample),
	}, nil
}

// Create creates and starts a new Firecracker VM.
func (p *Provider) Create(ctx context.Context, spec vmdomain.CreateSpec) (string, error) {
	logger := pkglog.FromContext(ctx)

	vmID := spec.InstanceID
	if vmID == "" {
		// Backward compatibility for call sites that haven't been updated yet.
		vmID = spec.EnvironmentID
	}
	vmDir := p.config.VMDir(vmID)
	socketPath := p.config.SocketPath(vmID)
	vsockPath := p.config.VsockPath(vmID)

	if vmID == "" {
		return "", fmt.Errorf("instance ID is required")
	}

	// Create VM directory
	if err := os.MkdirAll(vmDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create VM directory: %w", err)
	}
	_ = os.Remove(socketPath)
	_ = os.Remove(vsockPath)

	// Clone environment base image to a per-VM writable rootfs for isolation.
	vmRootfsPath := filepath.Join(vmDir, "rootfs.ext4")
	cloneMode, cloneFallbackReason, err := cloneRootfs(spec.ImagePath, vmRootfsPath)
	if err != nil {
		_ = os.RemoveAll(vmDir)
		return "", fmt.Errorf("failed to clone rootfs image: %w", err)
	}

	if cloneFallbackReason != "" {
		logger.Warn(
			"Rootfs reflink unavailable, using full copy",
			"vm_id", vmID,
			"source_image", spec.ImagePath,
			"destination_image", vmRootfsPath,
			"reason", cloneFallbackReason,
		)
	}
	logger.Info("Rootfs clone mode selected", "vm_id", vmID, "clone_mode", cloneMode, "source_image", spec.ImagePath, "destination_image", vmRootfsPath)

	fcCfg := fcsdk.Config{
		SocketPath:      socketPath,
		KernelImagePath: p.config.KernelPath,
		KernelArgs:      p.config.KernelArgs,
		Drives:          fcsdk.NewDrivesBuilder(vmRootfsPath).Build(),
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

	// Enable vsock if requested by runtime config or globally for exec API support.
	effectiveVsock := spec.Runtime.Vsock
	if effectiveVsock.GuestPort == 0 {
		effectiveVsock.GuestPort = p.config.GuestAgentPort
	}
	if effectiveVsock.HostUDSPath == "" {
		effectiveVsock.HostUDSPath = vsockPath
	}

	if p.config.ExecEnabled || effectiveVsock.Enabled {
		effectiveVsock.Enabled = true
		_ = os.Remove(effectiveVsock.HostUDSPath)

		guestCID, err := p.resolveGuestCID(vmID, effectiveVsock.GuestCID)
		if err != nil {
			_ = os.RemoveAll(vmDir)
			return "", fmt.Errorf("resolve guest cid: %w", err)
		}

		effectiveVsock.GuestCID = guestCID
		if err := p.persistGuestCID(vmID, guestCID); err != nil {
			_ = os.RemoveAll(vmDir)
			return "", fmt.Errorf("persist guest cid: %w", err)
		}

		fcCfg.VsockDevices = []fcsdk.VsockDevice{
			{
				ID:   "agent",
				Path: effectiveVsock.HostUDSPath,
				CID:  effectiveVsock.GuestCID,
			},
		}
	}

	cmd := fcsdk.VMCommandBuilder{}.
		WithBin(p.config.BinaryPath).
		WithSocketPath(socketPath).
		Build(context.Background())

	initCtx, initCancel := context.WithTimeout(context.Background(), time.Duration(p.config.SocketTimeout)*time.Second)
	defer initCancel()

	machine, err := fcsdk.NewMachine(initCtx, fcCfg, fcsdk.WithProcessRunner(cmd))
	if err != nil {
		_ = os.RemoveAll(vmDir)
		_ = p.releaseGuestCID(vmID)
		_ = os.Remove(effectiveVsock.HostUDSPath)
		return "", fmt.Errorf("failed to create machine: %w", err)
	}

	machineCtx, cancel := context.WithCancel(context.Background())
	if err := machine.Start(machineCtx); err != nil {
		cancel()
		_ = os.RemoveAll(vmDir)
		_ = p.releaseGuestCID(vmID)
		_ = os.Remove(effectiveVsock.HostUDSPath)
		return "", fmt.Errorf("failed to start firecracker: %w", err)
	}

	// Track the VM
	p.mu.Lock()
	p.vms[vmID] = &VMInstance{
		ID:         vmID,
		SocketPath: socketPath,
		VsockPath:  effectiveVsock.HostUDSPath,
		GuestCID:   effectiveVsock.GuestCID,
		GuestPort:  effectiveVsock.GuestPort,
		Machine:    machine,
		Cancel:     cancel,
		Config:     spec,
		StartedAt:  time.Now(),
	}
	p.mu.Unlock()

	pid, _ := machine.PID()
	logger.Info("Firecracker VM created", "vm_id", vmID, "pid", pid, "socket", socketPath, "vsock_path", effectiveVsock.HostUDSPath, "guest_cid", effectiveVsock.GuestCID, "guest_port", effectiveVsock.GuestPort, "kernel_args", p.config.KernelArgs, "rootfs_path", vmRootfsPath, "rootfs_clone_mode", cloneMode)
	return vmID, nil
}

// Start resumes a paused VM. Firecracker cannot restart a fully stopped VM.
func (p *Provider) Start(ctx context.Context, id string) error {
	logger := pkglog.FromContext(ctx)

	p.mu.RLock()
	vm, exists := p.vms[id]
	p.mu.RUnlock()

	if !exists {
		logger.Warn("Start: VM not found", "vm_id", id)
		return fmt.Errorf("VM not found: %s", id)
	}

	info, err := vm.Machine.DescribeInstanceInfo(ctx)
	if err != nil {
		logger.Warn("Start: failed to inspect VM state", "vm_id", id, "error", err)
		return fmt.Errorf("failed to inspect VM state: %w", err)
	}

	state := stringValue(info.State)
	switch state {
	case models.InstanceInfoStatePaused:
		logger.Debug("Start: resuming paused VM", "vm_id", id)
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
	if vm.VsockPath != "" {
		_ = os.Remove(vm.VsockPath)
	}
	_ = p.releaseGuestCID(id)

	logger.Info("Firecracker VM stopped", "vm_id", id)
	return nil
}

// Destroy forcefully terminates and removes a VM.
func (p *Provider) Destroy(ctx context.Context, id string) error {
	logger := pkglog.FromContext(ctx)

	p.mu.RLock()
	vm, exists := p.vms[id]
	p.mu.RUnlock()

	if !exists {
		logger.Warn("Destroy: VM not found", "vm_id", id)
		return fmt.Errorf("VM not found: %s", id)
	}

	_ = vm
	return p.cleanup(ctx, id, true)
}

// Status returns the current runtime status of the VM.
func (p *Provider) Status(ctx context.Context, id string) (vmdomain.RuntimeStatus, error) {
	logger := pkglog.FromContext(ctx)

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
		status := vmdomain.RuntimeStatus{
			ID:        id,
			State:     "stopped",
			PID:       0,
			VsockPath: vmInstance.VsockPath,
			GuestCID:  vmInstance.GuestCID,
			VCPU:      vmInstance.Config.Resources.VCPU,
			MemoryMB:  vmInstance.Config.Resources.MemoryMB,
			UptimeSec: uptime,
		}
		logger.Debug("Status: process not running", "vm_id", id, "state", status.State)
		return status, nil
	}

	instanceInfo, err := vmInstance.Machine.DescribeInstanceInfo(ctx)
	if err != nil {
		logger.Error("Status: failed to read VM status", "vm_id", id, "error", err)
		return vmdomain.RuntimeStatus{}, fmt.Errorf("failed to read VM status: %w", err)
	}

	status := vmdomain.RuntimeStatus{
		ID:        id,
		State:     p.mapState(stringValue(instanceInfo.State)),
		PID:       pid,
		VsockPath: vmInstance.VsockPath,
		GuestCID:  vmInstance.GuestCID,
		VCPU:      vmInstance.Config.Resources.VCPU,
		MemoryMB:  vmInstance.Config.Resources.MemoryMB,
		UptimeSec: uptime,
	}
	logger.Debug("Status: retrieved VM status", "vm_id", id, "state", status.State, "pid", pid, "uptime_sec", uptime)
	return status, nil
}

// Execute runs a command inside the VM via vsock.
func (p *Provider) Execute(ctx context.Context, id string, cmd []string) (string, string, int, error) {
	logger := pkglog.FromContext(ctx)

	p.mu.RLock()
	vm, exists := p.vms[id]
	p.mu.RUnlock()

	if !exists {
		logger.Warn("Execute: VM not found", "vm_id", id)
		return "", "", -1, fmt.Errorf("VM not found: %s", id)
	}
	if len(cmd) == 0 {
		return "", "", -1, fmt.Errorf("command is required")
	}
	if !p.config.ExecEnabled {
		logger.Warn("Execute: command execution is disabled", "vm_id", id)
		return "", "", -1, fmt.Errorf("command execution is disabled")
	}
	if vm.GuestCID == 0 || vm.GuestPort == 0 || vm.VsockPath == "" {
		return "", "", -1, fmt.Errorf("vsock command channel is not configured for VM %s", id)
	}

	logger.Debug("Execute: running command via vsock", "vm_id", id, "cmd", strings.Join(cmd, " "))
	return p.executeViaVsock(ctx, vm, cmd)
}

// GetMetrics returns resource usage metrics for the VM.
func (p *Provider) GetMetrics(ctx context.Context, id string) (vmdomain.Metrics, error) {
	logger := pkglog.FromContext(ctx)

	p.mu.RLock()
	vmInstance, exists := p.vms[id]
	p.mu.RUnlock()

	if !exists {
		logger.Warn("GetMetrics: VM not found", "vm_id", id)
		return vmdomain.Metrics{}, fmt.Errorf("VM not found: %s", id)
	}

	pid, err := vmInstance.Machine.PID()
	if err != nil || pid <= 0 {
		logger.Error("GetMetrics: failed to resolve VM PID", "vm_id", id, "error", err)
		return vmdomain.Metrics{}, fmt.Errorf("failed to resolve VM PID: %w", err)
	}

	procTicks, err := readProcTicks(pid)
	if err != nil {
		return vmdomain.Metrics{}, err
	}
	totalTicks, err := readTotalCPUTicks()
	if err != nil {
		return vmdomain.Metrics{}, err
	}
	memoryUsedMB, err := readProcessRSSMB(pid)
	if err != nil {
		return vmdomain.Metrics{}, err
	}
	readBytes, writeBytes, _ := readProcessIOBytes(pid)

	now := time.Now().UTC()
	cpuPercent := 0.0
	p.mu.Lock()
	if prev, ok := p.prev[id]; ok {
		deltaProc := procTicks - prev.procTicks
		deltaTotal := totalTicks - prev.totalTicks
		if deltaTotal > 0 {
			cpuPercent = (float64(deltaProc) / float64(deltaTotal)) * 100.0
			if cpuPercent < 0 {
				cpuPercent = 0
			}
		}
	}
	p.prev[id] = cpuSample{procTicks: procTicks, totalTicks: totalTicks, time: now}
	p.mu.Unlock()

	memoryLimit := vmInstance.Config.Resources.MemoryMB
	memoryPercent := 0.0
	if memoryLimit > 0 {
		memoryPercent = (float64(memoryUsedMB) / float64(memoryLimit)) * 100.0
		if memoryPercent > 100 {
			memoryPercent = 100
		}
	}

	diskUsedMB := int((readBytes + writeBytes) / (1024 * 1024))

	return vmdomain.Metrics{
		CPUUsagePercent:      cpuPercent,
		MemoryUsedMB:         memoryUsedMB,
		MemoryLimitMB:        memoryLimit,
		MemoryPercent:        memoryPercent,
		CollectedAt:          now.Unix(),
		TasksExecuted:        0,
		TasksFailed:          0,
		DiskUsedMB:           diskUsedMB,
		DiskLimitMB:          vmInstance.Config.Resources.DiskMB,
		DiskPercent:          0,
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
		delete(p.prev, id)
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

	if vm.VsockPath != "" {
		_ = os.Remove(vm.VsockPath)
	}
	_ = p.releaseGuestCID(id)

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

func readProcTicks(pid int) (uint64, error) {
	content, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, fmt.Errorf("read proc stat: %w", err)
	}
	fields := strings.Fields(string(content))
	if len(fields) < 17 {
		return 0, fmt.Errorf("invalid /proc/%d/stat format", pid)
	}
	utime, err := strconv.ParseUint(fields[13], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse utime: %w", err)
	}
	stime, err := strconv.ParseUint(fields[14], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse stime: %w", err)
	}
	return utime + stime, nil
}

func readTotalCPUTicks() (uint64, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, fmt.Errorf("open /proc/stat: %w", err)
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	if !s.Scan() {
		return 0, fmt.Errorf("empty /proc/stat")
	}
	line := s.Text()
	parts := strings.Fields(line)
	if len(parts) < 2 || parts[0] != "cpu" {
		return 0, fmt.Errorf("invalid /proc/stat cpu line")
	}

	var total uint64
	for _, v := range parts[1:] {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			continue
		}
		total += n
	}
	return total, nil
}

func readProcessRSSMB(pid int) (int, error) {
	content, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, fmt.Errorf("read proc status: %w", err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			break
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse VmRSS: %w", err)
		}
		return int(kb / 1024), nil
	}
	return 0, nil
}

func readProcessIOBytes(pid int) (uint64, uint64, error) {
	content, err := os.ReadFile(fmt.Sprintf("/proc/%d/io", pid))
	if err != nil {
		return 0, 0, err
	}
	var readBytes uint64
	var writeBytes uint64
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(line, "read_bytes:") {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				readBytes, _ = strconv.ParseUint(fields[1], 10, 64)
			}
		}
		if strings.HasPrefix(line, "write_bytes:") {
			fields := strings.Fields(line)
			if len(fields) == 2 {
				writeBytes, _ = strconv.ParseUint(fields[1], 10, 64)
			}
		}
	}
	return readBytes, writeBytes, nil
}

func copyFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open source image: %w", err)
	}
	defer src.Close()

	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("create destination image: %w", err)
	}

	if _, err := io.Copy(dst, src); err != nil {
		_ = dst.Close()
		return fmt.Errorf("copy image bytes: %w", err)
	}

	if err := dst.Sync(); err != nil {
		_ = dst.Close()
		return fmt.Errorf("sync destination image: %w", err)
	}

	if err := dst.Close(); err != nil {
		return fmt.Errorf("close destination image: %w", err)
	}

	return nil
}

func cloneRootfs(srcPath, dstPath string) (string, string, error) {
	if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
		return "", "", fmt.Errorf("create destination dir: %w", err)
	}

	_ = os.Remove(dstPath)

	// Try filesystem-level COW clone first. On supported filesystems (xfs/btrfs), this
	// avoids copying all bytes while keeping a raw disk image Firecracker can boot.
	reflinkErr := reflinkClone(srcPath, dstPath)
	if reflinkErr == nil {
		return "reflink", "", nil
	}

	if err := copyFile(srcPath, dstPath); err != nil {
		return "", "", err
	}

	return "copy", reflinkErr.Error(), nil
}

func reflinkClone(srcPath, dstPath string) error {
	cpPath, err := exec.LookPath("cp")
	if err != nil {
		return fmt.Errorf("cp not found: %w", err)
	}

	cmd := exec.Command(cpPath, "--reflink=always", "--sparse=always", srcPath, dstPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("reflink clone failed: %w: %s", err, msg)
		}
		return fmt.Errorf("reflink clone failed: %w", err)
	}

	return nil
}

func (p *Provider) executeViaVsock(ctx context.Context, vm *VMInstance, cmd []string) (string, string, int, error) {
	timeout, stdoutLimit, stderrLimit := p.resolveExecLimits(vm)
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(execCtx, "unix", vm.VsockPath)
	if err != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return "", "", -1, fmt.Errorf("connect guest agent timeout: %w", execCtx.Err())
		}
		return "", "", -1, fmt.Errorf("connect guest agent: %w", err)
	}
	defer conn.Close()

	if deadline, ok := execCtx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	if _, err := io.WriteString(conn, fmt.Sprintf("CONNECT %d\n", vm.GuestPort)); err != nil {
		return "", "", -1, fmt.Errorf("open guest vsock stream: %w", err)
	}

	ackReader := bufio.NewReader(conn)

	requestID := strconv.FormatInt(time.Now().UnixNano(), 10)
	request := execRequest{
		ProtocolVersion:  execProtocolVersion,
		RequestID:        requestID,
		Argv:             cmd,
		TimeoutMS:        timeout.Milliseconds(),
		StdoutLimitBytes: stdoutLimit,
		StderrLimitBytes: stderrLimit,
	}

	if err := writeFramedJSON(conn, request, 1024*1024); err != nil {
		return "", "", -1, fmt.Errorf("send execute request: %w", err)
	}

	maxResponsePayload := stdoutLimit + stderrLimit + 256*1024
	if maxResponsePayload < 1024*1024 {
		maxResponsePayload = 1024 * 1024
	}

	var response execResponse
	if err := p.readVsockExecResponse(ackReader, maxResponsePayload, &response); err != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			return "", "", -1, fmt.Errorf("execute command timeout: %w", execCtx.Err())
		}
		return "", "", -1, fmt.Errorf("read execute response: %w", err)
	}

	if response.ProtocolVersion != execProtocolVersion {
		return response.Stdout, response.Stderr, response.ExitCode, fmt.Errorf("unsupported protocol version %d", response.ProtocolVersion)
	}
	if response.RequestID != requestID {
		return response.Stdout, response.Stderr, response.ExitCode, fmt.Errorf("mismatched response request id")
	}

	if response.Status == ExecProtocolStatusError {
		errorCode := response.ErrorCode
		if errorCode == "" {
			errorCode = ExecProtocolErrorInternal
		}
		if response.ErrorMessage == "" {
			response.ErrorMessage = "guest agent execution failed"
		}
		return response.Stdout, response.Stderr, response.ExitCode, fmt.Errorf("guest agent error (%s): %s", errorCode, response.ErrorMessage)
	}

	if response.StdoutTruncated || response.StderrTruncated {
		return response.Stdout, response.Stderr, response.ExitCode, fmt.Errorf("command output exceeded configured limits")
	}

	if response.ExitCode != 0 {
		return response.Stdout, response.Stderr, response.ExitCode, fmt.Errorf("command exited with code %d", response.ExitCode)
	}

	return response.Stdout, response.Stderr, response.ExitCode, nil
}

func (p *Provider) readVsockExecResponse(reader *bufio.Reader, maxResponsePayload int, out *execResponse) error {
	// Some Firecracker versions prepend an ASCII "OK ...\n" acknowledgement after CONNECT.
	if prefix, err := reader.Peek(3); err == nil && string(prefix) == "OK " {
		ackLine, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read guest vsock ack: %w", err)
		}
		ackLine = strings.TrimSpace(ackLine)
		if !strings.HasPrefix(ackLine, "OK") {
			return fmt.Errorf("guest vsock connect failed: %s", ackLine)
		}
	}

	if err := readFramedJSON(reader, maxResponsePayload, out); err != nil {
		return err
	}

	return nil
}

func (p *Provider) resolveExecLimits(vm *VMInstance) (time.Duration, int, int) {
	timeout := p.config.DefaultExecTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if vm.Config.Runtime.Exec.Timeout > 0 {
		timeout = vm.Config.Runtime.Exec.Timeout
	}

	stdoutLimit := p.config.MaxStdoutBytes
	if stdoutLimit <= 0 {
		stdoutLimit = 5 * 1024 * 1024
	}
	if vm.Config.Runtime.Exec.MaxStdoutBytes > 0 {
		stdoutLimit = vm.Config.Runtime.Exec.MaxStdoutBytes
	}

	stderrLimit := p.config.MaxStderrBytes
	if stderrLimit <= 0 {
		stderrLimit = 5 * 1024 * 1024
	}
	if vm.Config.Runtime.Exec.MaxStderrBytes > 0 {
		stderrLimit = vm.Config.Runtime.Exec.MaxStderrBytes
	}

	return timeout, stdoutLimit, stderrLimit
}

func (p *Provider) resolveGuestCID(vmID string, requested uint32) (uint32, error) {
	persisted, err := p.loadPersistedGuestCIDs(vmID)
	if err != nil {
		return 0, err
	}

	inUse := func(candidate uint32) (bool, error) {
		p.mu.RLock()
		for runningVMID, running := range p.vms {
			if runningVMID == vmID {
				continue
			}
			if running.GuestCID == candidate {
				p.mu.RUnlock()
				return true, nil
			}
		}
		p.mu.RUnlock()

		_, used := persisted[candidate]
		return used, nil
	}

	if requested > 0 {
		used, err := inUse(requested)
		if err != nil {
			return 0, err
		}
		if used {
			return 0, fmt.Errorf("requested guest cid %d is already in use", requested)
		}
		return requested, nil
	}

	return AllocateDeterministicCID(vmID, p.config.CIDMin, p.config.CIDMax, inUse)
}

func (p *Provider) loadPersistedGuestCIDs(excludeVMID string) (map[uint32]struct{}, error) {
	entries, err := os.ReadDir(p.config.BaseDir)
	if err != nil {
		return nil, fmt.Errorf("scan vm base dir: %w", err)
	}

	used := make(map[uint32]struct{})
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if entry.Name() == excludeVMID {
			continue
		}

		cid, err := readPersistedGuestCID(p.cidMetadataPath(entry.Name()))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read persisted guest cid for vm %s: %w", entry.Name(), err)
		}

		if cid > 0 {
			used[cid] = struct{}{}
		}
	}

	return used, nil
}

func (p *Provider) cidMetadataPath(vmID string) string {
	return filepath.Join(p.config.VMDir(vmID), "guest.cid")
}

func (p *Provider) persistGuestCID(vmID string, cid uint32) error {
	if cid == 0 {
		return nil
	}

	content := strconv.FormatUint(uint64(cid), 10) + "\n"
	if err := os.WriteFile(p.cidMetadataPath(vmID), []byte(content), 0644); err != nil {
		return fmt.Errorf("write guest cid metadata: %w", err)
	}

	return nil
}

func (p *Provider) releaseGuestCID(vmID string) error {
	if err := os.Remove(p.cidMetadataPath(vmID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove guest cid metadata: %w", err)
	}

	return nil
}

func readPersistedGuestCID(path string) (uint32, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	raw := strings.TrimSpace(string(content))
	if raw == "" {
		return 0, fmt.Errorf("empty cid metadata")
	}

	parsed, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("parse cid metadata: %w", err)
	}

	return uint32(parsed), nil
}

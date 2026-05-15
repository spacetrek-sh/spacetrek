package firecracker

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

type baseLoopEntry struct {
	loopDev  string
	refCount int
}

type vmDeviceInfo struct {
	dmPath        string
	baseImagePath string
	cowLoopDev    string
}

// DmSnapshotManager manages dm-snapshot devices for incremental disk snapshots.
// It provides per-VM CoW devices layered over shared read-only base images.
type DmSnapshotManager struct {
	mu            sync.Mutex
	baseLoopDevs  map[string]*baseLoopEntry // baseImagePath → entry
	activeDevices map[string]*vmDeviceInfo  // vmID → device info
}

func NewDmSnapshotManager() *DmSnapshotManager {
	return &DmSnapshotManager{
		baseLoopDevs:  make(map[string]*baseLoopEntry),
		activeDevices: make(map[string]*vmDeviceInfo),
	}
}

// cowSizeForBase returns a reasonable CoW file size for a given base image.
// 10% of base with a 64MB floor — enough for typical inter-snapshot deltas
// without wasting space on a full-size sparse file.
func cowSizeForBase(baseSize int64) int64 {
	s := baseSize / 10
	if s < 64*1024*1024 {
		s = 64 * 1024 * 1024
	}
	return s
}

// actualDiskUsage returns the actual allocated disk space (in bytes) for a file,
// accounting for sparseness. Uses syscall.Stat_t.Blocks (512-byte units).
func actualDiskUsage(path string) (int64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, err
	}
	return st.Blocks * 512, nil
}

func (d *DmSnapshotManager) setupOriginLocked(baseImagePath string) (string, error) {
	if entry, ok := d.baseLoopDevs[baseImagePath]; ok {
		if _, err := os.Stat(entry.loopDev); err == nil {
			entry.refCount++
			return entry.loopDev, nil
		}
		delete(d.baseLoopDevs, baseImagePath)
	}

	out, err := exec.Command("losetup", "--find", "--show", "--read-only", baseImagePath).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("losetup --find --show --read-only %s: %w (%s)", baseImagePath, err, string(out))
	}
	loopDev := strings.TrimSpace(string(out))
	d.baseLoopDevs[baseImagePath] = &baseLoopEntry{loopDev: loopDev, refCount: 1}
	return loopDev, nil
}

func (d *DmSnapshotManager) SetupOrigin(baseImagePath string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.setupOriginLocked(baseImagePath)
}

func (d *DmSnapshotManager) CreateCoWDevice(vmID, baseImagePath, cowImagePath string) (string, error) {
	return d.createCowDevice(vmID, baseImagePath, cowImagePath, false)
}

func (d *DmSnapshotManager) ReconstructDevice(vmID, baseImagePath, cowImagePath string) (string, error) {
	return d.createCowDevice(vmID, baseImagePath, cowImagePath, true)
}

func (d *DmSnapshotManager) createCowDevice(vmID, baseImagePath, cowImagePath string, reuseExistingCow bool) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if info, ok := d.activeDevices[vmID]; ok {
		if _, err := os.Stat(info.dmPath); err == nil {
			return info.dmPath, nil
		}
		delete(d.activeDevices, vmID)
	}

	loopDev, err := d.setupOriginLocked(baseImagePath)
	if err != nil {
		return "", fmt.Errorf("setup origin for cow device: %w", err)
	}

	baseInfo, err := os.Stat(baseImagePath)
	if err != nil {
		return "", fmt.Errorf("stat base image %s: %w", baseImagePath, err)
	}
	baseSize := baseInfo.Size()

	if err := os.MkdirAll(filepath.Dir(cowImagePath), 0755); err != nil {
		return "", fmt.Errorf("create cow dir: %w", err)
	}

	if !reuseExistingCow {
		cowFile, err := os.OpenFile(cowImagePath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0644)
		if err != nil {
			return "", fmt.Errorf("create cow image: %w", err)
		}
		if err := cowFile.Truncate(cowSizeForBase(baseSize)); err != nil {
			cowFile.Close()
			return "", fmt.Errorf("truncate cow image: %w", err)
		}
		cowFile.Close()
	}

	out, err := exec.Command("losetup", "--find", "--show", cowImagePath).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("losetup cow %s: %w (%s)", cowImagePath, err, string(out))
	}
	cowLoopDev := strings.TrimSpace(string(out))

	dmName := dmNameForVM(vmID)
	sectors := baseSize / 512
	table := fmt.Sprintf("0 %d snapshot %s %s P 8", sectors, loopDev, cowLoopDev)

	out, err = exec.Command("dmsetup", "create", dmName, "--table", table).CombinedOutput()
	if err != nil {
		_ = exec.Command("losetup", "--detach", cowLoopDev).Run()
		return "", fmt.Errorf("dmsetup create %s: %w (%s)", dmName, err, string(out))
	}

	// Ensure /dev/mapper node exists (udev may not be running in containers).
	_ = exec.Command("dmsetup", "mknodes").Run()

	devicePath := "/dev/mapper/" + dmName
	d.activeDevices[vmID] = &vmDeviceInfo{
		dmPath:        devicePath,
		baseImagePath: baseImagePath,
		cowLoopDev:    cowLoopDev,
	}
	return devicePath, nil
}

func (d *DmSnapshotManager) SuspendDevice(vmID string) error {
	dmName := dmNameForVM(vmID)
	out, err := exec.Command("dmsetup", "suspend", dmName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("dmsetup suspend %s: %w (%s)", dmName, err, string(out))
	}
	return nil
}

// ResumeDevice resumes the dm-snapshot device after a suspend.
func (d *DmSnapshotManager) ResumeDevice(vmID string) error {
	dmName := dmNameForVM(vmID)
	out, err := exec.Command("dmsetup", "resume", dmName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("dmsetup resume %s: %w (%s)", dmName, err, string(out))
	}
	return nil
}

// RemoveDevice tears down the dm-snapshot device and detaches loop devices.
// Called on VM destroy.
func (d *DmSnapshotManager) RemoveDevice(vmID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	dmName := dmNameForVM(vmID)

	// Remove dm device.
	if out, err := exec.Command("dmsetup", "remove", "--force", dmName).CombinedOutput(); err != nil {
		_ = out
	}

	info, ok := d.activeDevices[vmID]
	if !ok {
		return nil
	}

	// Detach cow loop device.
	if info.cowLoopDev != "" {
		_ = exec.Command("losetup", "--detach", info.cowLoopDev).Run()
	}

	delete(d.activeDevices, vmID)

	// Decrement base image refcount using tracked base path.
	if entry, ok := d.baseLoopDevs[info.baseImagePath]; ok {
		entry.refCount--
		if entry.refCount <= 0 {
			_ = exec.Command("losetup", "--detach", entry.loopDev).Run()
			delete(d.baseLoopDevs, info.baseImagePath)
		}
	}
	return nil
}

// CleanupOrphans removes stale dm devices left from a previous crash.
// Called on provider startup.
func (d *DmSnapshotManager) CleanupOrphans() {
	out, err := exec.Command("dmsetup", "ls", "--target", "snapshot").CombinedOutput()
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "No") {
			continue
		}
		name := strings.SplitN(line, "	", 2)[0]
		if !strings.HasPrefix(name, "vm_") {
			continue
		}

		vmID := vmIDFromDmName(name)

		d.mu.Lock()
		_, active := d.activeDevices[vmID]
		d.mu.Unlock()
		if !active {
			_ = exec.Command("dmsetup", "remove", "--force", name).Run()
		}
	}
}

// PreflightCheck warns if available loop devices are below a safe threshold.
// Called on provider startup alongside CleanupOrphans.
func (d *DmSnapshotManager) PreflightCheck() {
	// Ensure loop device nodes exist (udev doesn't run in containers)
	for i := 0; i < 256; i++ {
		path := fmt.Sprintf("/dev/loop%d", i)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			_ = exec.Command("mknod", "-m", "0660", path, "b", "7", fmt.Sprint(i)).Run()
		}
	}

	if _, err := exec.Command("losetup", "-f").CombinedOutput(); err != nil {
		slog.Warn("dm-snapshot preflight: no free loop devices available", "error", err)
		return
	}

	if data, err := os.ReadFile("/sys/module/loop/parameters/max_loop"); err == nil {
		maxStr := strings.TrimSpace(string(data))
		slog.Info("dm-snapshot preflight: loop device capacity", "max_loop", maxStr)
	} else {
		slog.Info("dm-snapshot preflight: dynamic loop device allocation (no max_loop param)")
	}
}

// ResetCoW swaps the CoW backing file in-place using dmsetup reload.
// Must be called while the device is SUSPENDED — the caller is responsible
// for calling SuspendDevice before and ResumeDevice after.
func (d *DmSnapshotManager) ResetCoW(vmID, cowImagePath string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	info, ok := d.activeDevices[vmID]
	if !ok {
		return fmt.Errorf("no active dm device for vm %s", vmID)
	}

	dmName := dmNameForVM(vmID)
	baseEntry, ok := d.baseLoopDevs[info.baseImagePath]
	if !ok {
		return fmt.Errorf("no base loop device for vm %s", vmID)
	}

	// Detach old cow loop device.
	if info.cowLoopDev != "" {
		_ = exec.Command("losetup", "--detach", info.cowLoopDev).Run()
	}

	// Truncate cow file to fresh empty state.
	baseInfo, err := os.Stat(info.baseImagePath)
	if err != nil {
		return fmt.Errorf("stat base image for cow reset: %w", err)
	}
	cowFile, err := os.OpenFile(cowImagePath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("truncate cow image: %w", err)
	}
	if err := cowFile.Truncate(cowSizeForBase(baseInfo.Size())); err != nil {
		cowFile.Close()
		return fmt.Errorf("resize cow image: %w", err)
	}
	cowFile.Close()

	// Attach new cow file to a loop device.
	out, err := exec.Command("losetup", "--find", "--show", cowImagePath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("losetup new cow %s: %w (%s)", cowImagePath, err, string(out))
	}
	newCowLoopDev := strings.TrimSpace(string(out))

	// Reload the dm table in-place (device stays, table swaps).
	sectors := baseInfo.Size() / 512
	table := fmt.Sprintf("0 %d snapshot %s %s P 8", sectors, baseEntry.loopDev, newCowLoopDev)
	if out, err := exec.Command("dmsetup", "reload", dmName, "--table", table).CombinedOutput(); err != nil {
		_ = exec.Command("losetup", "--detach", newCowLoopDev).Run()
		return fmt.Errorf("dmsetup reload %s for cow reset: %w (%s)", dmName, err, string(out))
	}

	info.cowLoopDev = newCowLoopDev
	return nil
}

// Close detaches all base loop devices. Called on provider shutdown.
func (d *DmSnapshotManager) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, entry := range d.baseLoopDevs {
		_ = exec.Command("losetup", "--detach", entry.loopDev).Run()
	}
	d.baseLoopDevs = make(map[string]*baseLoopEntry)
}

// dmNameForVM returns the device-mapper name for a VM.
func dmNameForVM(vmID string) string {
	return "vm_" + strings.ReplaceAll(vmID, "-", "_")
}

// vmIDFromDmName reverses dmNameForVM, restoring original hyphens.
// Safe because VM IDs are UUIDs (only hyphens, no underscores).
func vmIDFromDmName(dmName string) string {
	return strings.ReplaceAll(dmName[3:], "_", "-")
}

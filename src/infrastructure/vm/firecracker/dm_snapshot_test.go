package firecracker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --- Unit tests for naming helpers ---

func TestDmNameForVM(t *testing.T) {
	tests := []struct {
		vmID string
		want string
	}{
		{"a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d", "vm_a1b2c3d4_e5f6_4a7b_8c9d_0e1f2a3b4c5d"},
		{"00000000-0000-0000-0000-000000000000", "vm_00000000_0000_0000_0000_000000000000"},
		{"f", "vm_f"},
	}
	for _, tt := range tests {
		got := dmNameForVM(tt.vmID)
		if got != tt.want {
			t.Errorf("dmNameForVM(%q) = %q, want %q", tt.vmID, got, tt.want)
		}
	}
}

func TestVmIDFromDmName(t *testing.T) {
	tests := []struct {
		dmName string
		want   string
	}{
		{"vm_a1b2c3d4_e5f6_4a7b_8c9d_0e1f2a3b4c5d", "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"},
		{"vm_00000000_0000_0000_0000_000000000000", "00000000-0000-0000-0000-000000000000"},
		{"vm_f", "f"},
	}
	for _, tt := range tests {
		got := vmIDFromDmName(tt.dmName)
		if got != tt.want {
			t.Errorf("vmIDFromDmName(%q) = %q, want %q", tt.dmName, got, tt.want)
		}
	}
}

func TestDmNameRoundTrip(t *testing.T) {
	// UUIDs only contain hex digits and hyphens, so the underscore↔hyphen mapping is unambiguous.
	vmIDs := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"6ba7b810-9dad-11d1-80b4-00c04fd430c8",
	}
	for _, id := range vmIDs {
		dmName := dmNameForVM(id)
		restored := vmIDFromDmName(dmName)
		if restored != id {
			t.Errorf("round-trip failed: %q → %q → %q", id, dmName, restored)
		}
	}
}

// --- Integration tests (require root + device-mapper) ---

func skipIfNotRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("skipping dm-snapshot integration test: requires root")
	}
	if _, err := exec.LookPath("dmsetup"); err != nil {
		t.Skip("skipping: dmsetup not found in PATH")
	}
	if _, err := exec.LookPath("losetup"); err != nil {
		t.Skip("skipping: losetup not found in PATH")
	}
}

func TestDmSnapshotLifecycle(t *testing.T) {
	skipIfNotRoot(t)

	mgr := NewDmSnapshotManager()
	defer mgr.Close()

	dir := t.TempDir()
	baseImage := filepath.Join(dir, "base.ext4")

	// Create a small 4MB base image.
	f, err := os.Create(baseImage)
	if err != nil {
		t.Fatalf("create base image: %v", err)
	}
	if err := f.Truncate(4 * 1024 * 1024); err != nil {
		f.Close()
		t.Fatalf("truncate base image: %v", err)
	}
	f.Close()

	vmID := "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"
	cowPath := filepath.Join(dir, "cow.img")

	// Step 1: SetupOrigin — should succeed and return a loop device.
	loopDev, err := mgr.SetupOrigin(baseImage)
	if err != nil {
		t.Fatalf("SetupOrigin: %v", err)
	}
	if !strings.HasPrefix(loopDev, "/dev/loop") {
		t.Fatalf("expected loop device, got %q", loopDev)
	}

	// Step 2: CreateCoWDevice — should create /dev/mapper/vm_{id}.
	dmPath, err := mgr.CreateCoWDevice(vmID, baseImage, cowPath)
	if err != nil {
		t.Fatalf("CreateCoWDevice: %v", err)
	}
	expectedDmPath := "/dev/mapper/vm_a1b2c3d4_e5f6_4a7b_8c9d_0e1f2a3b4c5d"
	if dmPath != expectedDmPath {
		t.Fatalf("expected dm path %q, got %q", expectedDmPath, dmPath)
	}

	// Verify the device actually exists.
	if _, err := os.Stat(dmPath); err != nil {
		t.Fatalf("dm device not found at %q: %v", dmPath, err)
	}

	// Step 3: Write some data to the dm device to trigger CoW.
	data := []byte("test data for dm-snapshot")
	if err := os.WriteFile(dmPath, data, 0644); err != nil {
		// Writing directly to block device may need dd; try that.
		ddCmd := exec.Command("dd", "if=/dev/zero", "of="+dmPath, "bs=4k", "count=1", "conv=notrunc")
		if out, ddErr := ddCmd.CombinedOutput(); ddErr != nil {
			t.Fatalf("write to dm device failed: %v (dd: %v: %s)", err, ddErr, string(out))
		}
	}

	// Step 4: Suspend the device.
	if err := mgr.SuspendDevice(vmID); err != nil {
		t.Fatalf("SuspendDevice: %v", err)
	}

	// Step 5: Copy the cow file out (simulating snapshot capture).
	snapshotCow := filepath.Join(dir, "cow_snap1.img")
	cowData, err := os.ReadFile(cowPath)
	if err != nil {
		t.Fatalf("read cow file: %v", err)
	}
	if err := os.WriteFile(snapshotCow, cowData, 0644); err != nil {
		t.Fatalf("copy cow snapshot: %v", err)
	}

	// Step 6: Resume the device.
	if err := mgr.ResumeDevice(vmID); err != nil {
		t.Fatalf("ResumeDevice: %v", err)
	}

	// Step 7: Remove the device — should clean up everything.
	if err := mgr.RemoveDevice(vmID); err != nil {
		t.Fatalf("RemoveDevice: %v", err)
	}

	// Verify device is gone.
	if _, err := os.Stat(dmPath); !os.IsNotExist(err) {
		t.Fatalf("expected dm device to be removed, but it still exists")
	}
}

func TestDmSnapshotReconstruct(t *testing.T) {
	skipIfNotRoot(t)

	mgr := NewDmSnapshotManager()
	defer mgr.Close()

	dir := t.TempDir()
	baseImage := filepath.Join(dir, "base.ext4")

	f, err := os.Create(baseImage)
	if err != nil {
		t.Fatalf("create base image: %v", err)
	}
	if err := f.Truncate(4 * 1024 * 1024); err != nil {
		f.Close()
		t.Fatalf("truncate base image: %v", err)
	}
	f.Close()

	vmID := "b2c3d4e5-f6a7-4b8c-9d0e-1f2a3b4c5d6e"
	cowPath := filepath.Join(dir, "cow.img")

	// Create a CoW device and capture the cow file.
	dmPath1, err := mgr.CreateCoWDevice(vmID, baseImage, cowPath)
	if err != nil {
		t.Fatalf("CreateCoWDevice: %v", err)
	}

	// Write to trigger CoW entries.
	ddCmd := exec.Command("dd", "if=/dev/zero", "of="+dmPath1, "bs=4k", "count=2", "conv=notrunc")
	if out, err := ddCmd.CombinedOutput(); err != nil {
		t.Fatalf("dd write: %v (%s)", err, string(out))
	}

	// Suspend and save cow.
	if err := mgr.SuspendDevice(vmID); err != nil {
		t.Fatalf("SuspendDevice: %v", err)
	}
	savedCow := filepath.Join(dir, "cow_saved.img")
	cowData, err := os.ReadFile(cowPath)
	if err != nil {
		t.Fatalf("read cow: %v", err)
	}
	if err := os.WriteFile(savedCow, cowData, 0644); err != nil {
		t.Fatalf("save cow snapshot: %v", err)
	}
	if err := mgr.ResumeDevice(vmID); err != nil {
		t.Fatalf("ResumeDevice: %v", err)
	}
	// Tear down.
	if err := mgr.RemoveDevice(vmID); err != nil {
		t.Fatalf("RemoveDevice: %v", err)
	}

	// Now reconstruct from the saved cow file.
	reconCow := filepath.Join(dir, "cow_recon.img")
	if err := os.WriteFile(reconCow, cowData, 0644); err != nil {
		t.Fatalf("write recon cow: %v", err)
	}

	dmPath2, err := mgr.ReconstructDevice(vmID, baseImage, reconCow)
	if err != nil {
		t.Fatalf("ReconstructDevice: %v", err)
	}

	expectedDmPath := "/dev/mapper/vm_b2c3d4e5_f6a7_4b8c_9d0e_1f2a3b4c5d6e"
	if dmPath2 != expectedDmPath {
		t.Fatalf("expected dm path %q, got %q", expectedDmPath, dmPath2)
	}
	if _, err := os.Stat(dmPath2); err != nil {
		t.Fatalf("reconstructed dm device not found: %v", err)
	}

	// Clean up.
	if err := mgr.RemoveDevice(vmID); err != nil {
		t.Fatalf("RemoveDevice after reconstruct: %v", err)
	}
}

func TestDmSnapshotBaseRefcounting(t *testing.T) {
	skipIfNotRoot(t)

	mgr := NewDmSnapshotManager()
	defer mgr.Close()

	dir := t.TempDir()
	baseImage := filepath.Join(dir, "base.ext4")

	f, err := os.Create(baseImage)
	if err != nil {
		t.Fatalf("create base image: %v", err)
	}
	if err := f.Truncate(4 * 1024 * 1024); err != nil {
		f.Close()
		t.Fatalf("truncate base image: %v", err)
	}
	f.Close()

	vm1 := "11111111-1111-4111-8111-111111111111"
	vm2 := "22222222-2222-4222-8222-222222222222"
	cow1 := filepath.Join(dir, "cow1.img")
	cow2 := filepath.Join(dir, "cow2.img")

	// CreateCoWDevice calls setupOriginLocked internally, so each VM increments refcount by 1.
	_, err = mgr.CreateCoWDevice(vm1, baseImage, cow1)
	if err != nil {
		t.Fatalf("CreateCoWDevice vm1: %v", err)
	}

	_, err = mgr.CreateCoWDevice(vm2, baseImage, cow2)
	if err != nil {
		t.Fatalf("CreateCoWDevice vm2: %v", err)
	}

	// Both VMs should share the same loop device — refcount is 2.
	mgr.mu.Lock()
	entry := mgr.baseLoopDevs[baseImage]
	refCount := entry.refCount
	mgr.mu.Unlock()
	if refCount != 2 {
		t.Fatalf("expected refcount 2, got %d", refCount)
	}

	// Remove first VM — loop device should still be alive (refcount drops to 1).
	if err := mgr.RemoveDevice(vm1); err != nil {
		t.Fatalf("RemoveDevice vm1: %v", err)
	}

	mgr.mu.Lock()
	loopDev := mgr.baseLoopDevs[baseImage].loopDev
	refCount = mgr.baseLoopDevs[baseImage].refCount
	mgr.mu.Unlock()

	if _, err := os.Stat(loopDev); err != nil {
		t.Fatalf("loop device should still exist after removing one VM: %v", err)
	}
	if refCount != 1 {
		t.Fatalf("expected refcount 1 after removing vm1, got %d", refCount)
	}

	// Remove second VM — loop device should be detached (refcount drops to 0).
	if err := mgr.RemoveDevice(vm2); err != nil {
		t.Fatalf("RemoveDevice vm2: %v", err)
	}

	mgr.mu.Lock()
	_, exists := mgr.baseLoopDevs[baseImage]
	mgr.mu.Unlock()
	if exists {
		t.Fatal("expected base loop entry to be removed when refcount hits 0")
	}
}

func TestDmSnapshotCleanupOrphans(t *testing.T) {
	skipIfNotRoot(t)

	mgr := NewDmSnapshotManager()
	defer mgr.Close()

	dir := t.TempDir()
	baseImage := filepath.Join(dir, "base.ext4")

	f, err := os.Create(baseImage)
	if err != nil {
		t.Fatalf("create base image: %v", err)
	}
	if err := f.Truncate(4 * 1024 * 1024); err != nil {
		f.Close()
		t.Fatalf("truncate base image: %v", err)
	}
	f.Close()

	vmID := "33333333-3333-4333-8333-333333333333"
	cowPath := filepath.Join(dir, "cow.img")

	// Create a device but don't register it in activeDevices (simulates orphan from crash).
	dmName := dmNameForVM(vmID)
	_, err = mgr.CreateCoWDevice(vmID, baseImage, cowPath)
	if err != nil {
		t.Fatalf("CreateCoWDevice: %v", err)
	}

	// Manually remove from activeDevices to simulate orphan.
	mgr.mu.Lock()
	delete(mgr.activeDevices, vmID)
	mgr.mu.Unlock()

	// CleanupOrphans should remove the stale device.
	mgr.CleanupOrphans()

	// Verify the dm device is gone.
	dmPath := "/dev/mapper/" + dmName
	if _, err := os.Stat(dmPath); !os.IsNotExist(err) {
		t.Fatalf("expected orphan dm device to be cleaned up, but it still exists")
	}
}

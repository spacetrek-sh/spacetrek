package firecracker

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestFlattenSparseFile verifies that flattenDmDevice correctly copies a file
// with data-hole-data pattern and preserves sparseness in the output.
func TestFlattenSparseFile(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src")
	dstPath := filepath.Join(dir, "dst")

	// Create a sparse source file: 1 MiB data, 2 MiB hole, 1 MiB data.
	const mb = 1 << 20
	totalSize := int64(4 * mb)

	f, err := os.Create(srcPath)
	if err != nil {
		t.Fatal(err)
	}

	// Write first 1 MiB of data (pattern 0xAA).
	block1 := bytes.Repeat([]byte{0xAA}, mb)
	if _, err := f.Write(block1); err != nil {
		t.Fatal(err)
	}

	// Seek past the 2 MiB hole.
	if _, err := f.Seek(3*mb, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	// Write last 1 MiB of data (pattern 0xBB).
	block2 := bytes.Repeat([]byte{0xBB}, mb)
	if _, err := f.Write(block2); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Flatten.
	if err := flattenDmDevice(srcPath, dstPath, totalSize); err != nil {
		t.Fatalf("flattenDmDevice: %v", err)
	}

	// Verify output size.
	dstInfo, err := os.Stat(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if dstInfo.Size() != totalSize {
		t.Fatalf("dst size = %d, want %d", dstInfo.Size(), totalSize)
	}

	// Verify content matches.
	srcData, _ := os.ReadFile(srcPath)
	dstData, _ := os.ReadFile(dstPath)
	if !bytes.Equal(srcData, dstData) {
		t.Fatal("dst content does not match src")
	}

	// Verify sparseness: actual disk usage should be less than logical size.
	var st syscall.Stat_t
	if err := syscall.Stat(dstPath, &st); err != nil {
		t.Fatal(err)
	}
	allocatedBytes := st.Blocks * 512
	// The hole (2 MiB) should not be allocated. Allow some overhead for
	// filesystem metadata, but allocated should be well under total.
	if allocatedBytes >= totalSize {
		t.Errorf("dst not sparse: allocated %d bytes >= logical %d bytes", allocatedBytes, totalSize)
	}
	t.Logf("sparse copy: logical=%d allocated=%d (saved %.0f%%)",
		totalSize, allocatedBytes, 100*float64(totalSize-allocatedBytes)/float64(totalSize))
}

// TestFlattenEmptyFile verifies that an all-hole (empty sparse) source produces
// a correctly-sized empty sparse output.
func TestFlattenEmptyFile(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src")
	dstPath := filepath.Join(dir, "dst")

	const totalSize = 4 * 1024 * 1024 // 4 MiB

	// Create an empty sparse file (all holes).
	f, err := os.Create(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(totalSize); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Flatten.
	if err := flattenDmDevice(srcPath, dstPath, totalSize); err != nil {
		t.Fatalf("flattenDmDevice: %v", err)
	}

	// Verify size.
	dstInfo, err := os.Stat(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if dstInfo.Size() != totalSize {
		t.Fatalf("dst size = %d, want %d", dstInfo.Size(), totalSize)
	}

	// Verify actual disk usage is minimal (just metadata, no data blocks).
	var st syscall.Stat_t
	if err := syscall.Stat(dstPath, &st); err != nil {
		t.Fatal(err)
	}
	allocatedBytes := st.Blocks * 512
	// An empty sparse file should have near-zero allocation.
	if allocatedBytes > 64*1024 { // allow up to 64 KiB for fs overhead
		t.Errorf("dst allocated %d bytes for empty file, expected near-zero", allocatedBytes)
	}
}

// TestFlattenFullFile verifies that a fully dense (no holes) source is copied
// completely and byte-for-byte identical.
func TestFlattenFullFile(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src")
	dstPath := filepath.Join(dir, "dst")

	const totalSize = 2 * 1024 * 1024 // 2 MiB

	// Create a fully dense file.
	data := bytes.Repeat([]byte{0xCC}, totalSize)
	if err := os.WriteFile(srcPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Flatten.
	if err := flattenDmDevice(srcPath, dstPath, totalSize); err != nil {
		t.Fatalf("flattenDmDevice: %v", err)
	}

	// Verify content matches exactly.
	dstData, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, dstData) {
		t.Fatal("dst content does not match src")
	}
}

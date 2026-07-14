package firecracker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCompressDecompressRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	compressed := filepath.Join(dir, "src.zst")
	decompressed := filepath.Join(dir, "src.restored.bin")

	original := []byte("hello incremental snapshots via dm-snapshot")
	if err := os.WriteFile(src, original, 0644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	compressedSize, err := CompressFileZstd(src, compressed)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if compressedSize <= 0 {
		t.Fatalf("expected positive compressed size, got %d", compressedSize)
	}

	if err := DecompressFileZstd(compressed, decompressed); err != nil {
		t.Fatalf("decompress: %v", err)
	}

	restored, err := os.ReadFile(decompressed)
	if err != nil {
		t.Fatalf("read decompressed: %v", err)
	}

	if string(restored) != string(original) {
		t.Fatalf("round-trip mismatch:\n  want: %q\n  got:  %q", original, restored)
	}
}

func TestCompressLargeContent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "large.bin")
	compressed := filepath.Join(dir, "large.zst")
	decompressed := filepath.Join(dir, "large.restored.bin")

	// 1MB of repeating data — compresses well with zstd.
	original := make([]byte, 1024*1024)
	for i := range original {
		original[i] = byte(i % 256)
	}
	if err := os.WriteFile(src, original, 0644); err != nil {
		t.Fatalf("write large src: %v", err)
	}

	compressedSize, err := CompressFileZstd(src, compressed)
	if err != nil {
		t.Fatalf("compress large: %v", err)
	}

	if compressedSize >= int64(len(original)) {
		t.Fatalf("compressed size (%d) should be smaller than original (%d) for repeating data", compressedSize, len(original))
	}

	if err := DecompressFileZstd(compressed, decompressed); err != nil {
		t.Fatalf("decompress large: %v", err)
	}

	restored, err := os.ReadFile(decompressed)
	if err != nil {
		t.Fatalf("read decompressed large: %v", err)
	}

	if len(restored) != len(original) {
		t.Fatalf("size mismatch: want %d, got %d", len(original), len(restored))
	}

	for i := range original {
		if restored[i] != original[i] {
			t.Fatalf("byte mismatch at offset %d: want %02x, got %02x", i, original[i], restored[i])
		}
	}
}

func TestCompressEmptyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "empty.bin")
	compressed := filepath.Join(dir, "empty.zst")
	decompressed := filepath.Join(dir, "empty.restored.bin")

	if err := os.WriteFile(src, []byte{}, 0644); err != nil {
		t.Fatalf("write empty src: %v", err)
	}

	compressedSize, err := CompressFileZstd(src, compressed)
	if err != nil {
		t.Fatalf("compress empty: %v", err)
	}
	// zstd frame header for empty content is still a few bytes.
	if compressedSize <= 0 {
		t.Fatalf("expected positive compressed size for empty file, got %d", compressedSize)
	}

	if err := DecompressFileZstd(compressed, decompressed); err != nil {
		t.Fatalf("decompress empty: %v", err)
	}

	fi, err := os.Stat(decompressed)
	if err != nil {
		t.Fatalf("stat decompressed: %v", err)
	}
	if fi.Size() != 0 {
		t.Fatalf("expected empty file after round-trip, got %d bytes", fi.Size())
	}
}

func TestCompressBinaryContent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "binary.bin")
	compressed := filepath.Join(dir, "binary.zst")
	decompressed := filepath.Join(dir, "binary.restored.bin")

	// All byte values 0x00–0xFF repeated a few times.
	original := make([]byte, 256*4)
	for i := range original {
		original[i] = byte(i % 256)
	}
	if err := os.WriteFile(src, original, 0644); err != nil {
		t.Fatalf("write binary src: %v", err)
	}

	if _, err := CompressFileZstd(src, compressed); err != nil {
		t.Fatalf("compress binary: %v", err)
	}

	if err := DecompressFileZstd(compressed, decompressed); err != nil {
		t.Fatalf("decompress binary: %v", err)
	}

	restored, err := os.ReadFile(decompressed)
	if err != nil {
		t.Fatalf("read decompressed binary: %v", err)
	}

	if string(restored) != string(original) {
		t.Fatalf("binary round-trip mismatch")
	}
}

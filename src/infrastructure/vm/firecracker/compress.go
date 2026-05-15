package firecracker

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/sys/unix"
)

// SparseRegion represents a contiguous data region in a sparse file
// (between SEEK_DATA and SEEK_HOLE boundaries).
type SparseRegion struct {
	Offset int64 `json:"offset"`
	Length int64 `json:"length"`
}

// ExtractSparseRegions scans a sparse file and returns all data (non-hole)
// regions using SEEK_DATA/SEEK_HOLE. Must be called BEFORE compression,
// while the file is still sparse.
func ExtractSparseRegions(path string) ([]SparseRegion, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open for sparse extraction: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat for sparse extraction: %w", err)
	}
	fileSize := fi.Size()
	if fileSize == 0 {
		return nil, nil
	}

	fd := int(f.Fd())
	var regions []SparseRegion
	offset := int64(0)

	for offset < fileSize {
		dataOff, err := unix.Seek(fd, offset, unix.SEEK_DATA)
		if err != nil {
			break // ENXIO: no more data regions
		}

		holeOff, err := unix.Seek(fd, dataOff, unix.SEEK_HOLE)
		if err != nil {
			holeOff = fileSize // treat rest as data
		}

		regions = append(regions, SparseRegion{
			Offset: dataOff,
			Length: holeOff - dataOff,
		})
		offset = holeOff
	}

	return regions, nil
}

// WriteManifest serializes sparse regions to a JSON file.
func WriteManifest(path string, regions []SparseRegion) error {
	data, err := json.Marshal(regions)
	if err != nil {
		return fmt.Errorf("marshal sparse regions: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// ReadManifest reads and deserializes sparse regions from a JSON file.
func ReadManifest(path string) ([]SparseRegion, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var regions []SparseRegion
	if err := json.Unmarshal(data, &regions); err != nil {
		return nil, fmt.Errorf("unmarshal manifest: %w", err)
	}
	return regions, nil
}

// MergeDiffMemory copies basePath → outputPath, then overwrites dirty
// regions from diffPath using the manifest. The result is a complete
// memory file Firecracker can load as a full (non-diff) snapshot.
func MergeDiffMemory(basePath, diffPath, outputPath string, manifest []SparseRegion) error {
	if err := copyFileLocal(basePath, outputPath); err != nil {
		return fmt.Errorf("copy base memory: %w", err)
	}

	out, err := os.OpenFile(outputPath, os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open merged output: %w", err)
	}
	defer out.Close()

	diff, err := os.Open(diffPath)
	if err != nil {
		return fmt.Errorf("open diff memory: %w", err)
	}
	defer diff.Close()

	for _, region := range manifest {
		buf := make([]byte, region.Length)
		if _, err := diff.ReadAt(buf, region.Offset); err != nil {
			return fmt.Errorf("read diff at %d: %w", region.Offset, err)
		}
		if _, err := out.WriteAt(buf, region.Offset); err != nil {
			return fmt.Errorf("write merged at %d: %w", region.Offset, err)
		}
	}

	return out.Sync()
}

// copyFileLocal is a plain file copy (no sparseness needed).
func copyFileLocal(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// CompressFileZstd reads srcPath and writes it compressed to dstPath.
// Returns the compressed file size.
func CompressFileZstd(srcPath, dstPath string) (int64, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return 0, err
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return 0, err
	}
	defer dst.Close()

	enc, err := zstd.NewWriter(dst, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return 0, err
	}

	_, err = io.Copy(enc, src)
	if err != nil {
		return 0, err
	}

	if err := enc.Close(); err != nil {
		return 0, err
	}

	fi, err := dst.Stat()
	if err != nil {
		return 0, err
	}

	return fi.Size(), nil
}

// DecompressFileZstd reads a zstd-compressed srcPath and writes decompressed data to dstPath.
func DecompressFileZstd(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer dst.Close()

	dec, err := zstd.NewReader(src)
	if err != nil {
		return err
	}
	defer dec.Close()

	_, err = io.Copy(dst, dec)
	return err
}

package firecracker

import (
	"fmt"
	"io"
	"os"
	"syscall"
	"unsafe"
)

const (
	seekData = 3 // SEEK_DATA
	seekHole = 4 // SEEK_HOLE
)

// flattenDmDevice reads the fully-resolved view of a stacked dm device (or any
// block device / regular file) and writes a sparse copy into dstPath. Only data
// extents are written; holes are preserved so the output stays sparse.
//
// This is the disk equivalent of MergeDiffMemory — it produces a self-contained
// image with no dependency on intermediate CoW chain layers.
func flattenDmDevice(srcDevice, dstPath string, sizeBytes int64) error {
	src, err := os.Open(srcDevice)
	if err != nil {
		return fmt.Errorf("flatten: open src %s: %w", srcDevice, err)
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("flatten: create dst %s: %w", dstPath, err)
	}
	defer dst.Close()

	if err := dst.Truncate(sizeBytes); err != nil {
		return fmt.Errorf("flatten: truncate dst: %w", err)
	}

	var offset int64
	buf := make([]byte, 1<<20) // 1 MiB

	for offset < sizeBytes {
		dataStart, err := seekFd(int(src.Fd()), offset, seekData)
		if err != nil {
			if isENXIO(err) {
				break // no more data regions
			}
			if isEINVAL(err) {
				// Block devices often do not support SEEK_DATA/SEEK_HOLE.
				// Fall back to a linear read that manually detects and skips zero-blocks.
				return linearSparseCopy(src, dst, offset, sizeBytes, buf)
			}
			return fmt.Errorf("flatten: SEEK_DATA at %d: %w", offset, err)
		}

		holeStart, err := seekFd(int(src.Fd()), dataStart, seekHole)
		if err != nil {
			if isENXIO(err) {
				holeStart = sizeBytes
			} else {
				return fmt.Errorf("flatten: SEEK_HOLE at %d: %w", dataStart, err)
			}
		}

		if err := copyDataExtent(src, dst, dataStart, holeStart, buf); err != nil {
			return fmt.Errorf("flatten: copy [%d,%d): %w", dataStart, holeStart, err)
		}

		offset = holeStart
	}

	return dst.Sync()
}

// linearSparseCopy reads src sequentially and writes non-zero blocks to dst.
// Used as a fallback when the source file/device does not support SEEK_DATA.
func linearSparseCopy(src, dst *os.File, startOffset, sizeBytes int64, buf []byte) error {
	if _, err := src.Seek(startOffset, io.SeekStart); err != nil {
		return fmt.Errorf("linearSparseCopy seek: %w", err)
	}

	offset := startOffset
	for offset < sizeBytes {
		n, err := io.ReadFull(src, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return fmt.Errorf("linearSparseCopy read at %d: %w", offset, err)
		}
		if n == 0 {
			break
		}

		// Process the read chunk in 4KiB blocks (standard page size)
		chunk := buf[:n]
		for i := 0; i < n; i += 4096 {
			end := i + 4096
			if end > n {
				end = n
			}
			block := chunk[i:end]
			
			if !isAllZeros(block) {
				if _, werr := dst.WriteAt(block, offset+int64(i)); werr != nil {
					return fmt.Errorf("linearSparseCopy write at %d: %w", offset+int64(i), werr)
				}
			}
		}

		offset += int64(n)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
	}
	return nil
}

func isAllZeros(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// copyDataExtent copies bytes from src to dst in the range [start, end).
func copyDataExtent(src, dst *os.File, start, end int64, buf []byte) error {
	if _, err := src.Seek(start, io.SeekStart); err != nil {
		return err
	}
	off := start
	for off < end {
		n := int64(len(buf))
		if end-off < n {
			n = end - off
		}
		rn, err := src.Read(buf[:n])
		if rn > 0 {
			if _, werr := syscall.Pwrite(int(dst.Fd()), buf[:rn], off); werr != nil {
				return werr
			}
			off += int64(rn)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}
	return nil
}

// seekFd performs lseek(2) with the given whence (SEEK_DATA or SEEK_HOLE).
func seekFd(fd int, offset int64, whence int) (int64, error) {
	n, _, errno := syscall.Syscall(syscall.SYS_LSEEK,
		uintptr(fd), uintptr(offset), uintptr(whence))
	if errno != 0 {
		return 0, errno
	}
	return int64(n), nil
}

// isENXIO returns true if the error is ENXIO (no more data/hole regions).
func isENXIO(err error) bool {
	errno, ok := err.(syscall.Errno)
	return ok && errno == syscall.ENXIO
}

// isEINVAL returns true if the error is EINVAL.
func isEINVAL(err error) bool {
	errno, ok := err.(syscall.Errno)
	return ok && errno == syscall.EINVAL
}

// blockDeviceSize returns the logical size of a block device via BLKGETSIZE64.
func blockDeviceSize(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var size uint64
	const blkgetsize64 = 0x80081272
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL,
		f.Fd(), blkgetsize64, uintptr(unsafe.Pointer(&size)))
	if errno != 0 {
		return 0, fmt.Errorf("BLKGETSIZE64 %s: %w", path, errno)
	}
	return int64(size), nil
}
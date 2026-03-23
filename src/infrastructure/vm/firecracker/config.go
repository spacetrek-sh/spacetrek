// Package firecracker provides Firecracker configuration.
package firecracker

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Config holds the Firecracker provider configuration.
type Config struct {
	// Paths
	BinaryPath string `json:"binary_path" env:"FIRECRACKER_BINARY_PATH"`
	KernelPath string `json:"kernel_path" env:"FIRECRACKER_KERNEL_PATH"`
	BaseDir    string `json:"base_dir" env:"FIRECRACKER_BASE_DIR"` // Where VMs are created
	KernelArgs string `json:"kernel_args" env:"FIRECRACKER_KERNEL_ARGS"`

	// Network
	MacAddress string `json:"mac_address" env:"FIRECRACKER_MAC_ADDRESS"`

	// Timeouts
	SocketTimeout   int `json:"socket_timeout" env:"FIRECRACKER_SOCKET_TIMEOUT"`     // in seconds
	ShutdownTimeout int `json:"shutdown_timeout" env:"FIRECRACKER_SHUTDOWN_TIMEOUT"` // in seconds

	// Features
	SMT        bool `json:"smt" env:"FIRECRACKER_SMT"`                 // Simultaneous Multithreading
	EnableMmds bool `json:"enable_mmds" env:"FIRECRACKER_ENABLE_MMDS"` // MicroVM Metadata Service
}

// DefaultConfig returns the default Firecracker configuration.
func DefaultConfig() Config {
	// Default kernel path (would be set properly in production)
	kernelPath := os.Getenv("FIRECRACKER_KERNEL_PATH")
	if kernelPath == "" {
		kernelPath = "/usr/share/firecracker/vmlinux"
	}

	baseDir := os.Getenv("FIRECRACKER_BASE_DIR")
	if baseDir == "" {
		baseDir = "/var/lib/firecracker/vms"
	}

	return Config{
		BinaryPath:      "firecracker",
		KernelPath:      kernelPath,
		BaseDir:         baseDir,
		KernelArgs:      "console=ttyS0 reboot=k panic=1 pci=off",
		MacAddress:      "02:FC:00:00:00:01",
		SocketTimeout:   30,
		ShutdownTimeout: 10,
		SMT:             false,
		EnableMmds:      true,
	}
}

// Validate checks if the configuration is valid.
func (c Config) Validate() error {
	if c.BinaryPath == "" {
		return fmt.Errorf("binary_path is required")
	}
	// For absolute paths, check file existence and executable permission directly
	// For relative paths, use LookPath to search PATH
	if filepath.IsAbs(c.BinaryPath) {
		if info, err := os.Stat(c.BinaryPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("firecracker binary does not exist: %s", c.BinaryPath)
			}
			return fmt.Errorf("failed to access firecracker binary: %w", err)
		} else if info.Mode().Perm()&0111 == 0 {
			return fmt.Errorf("firecracker binary is not executable: %s", c.BinaryPath)
		}
	} else {
		if _, err := exec.LookPath(c.BinaryPath); err != nil {
			return fmt.Errorf("firecracker binary not found in PATH: %s", c.BinaryPath)
		}
	}

	if c.KernelPath == "" {
		return fmt.Errorf("kernel_path is required")
	}
	if _, err := os.Stat(c.KernelPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("kernel_path does not exist: %s", c.KernelPath)
	}

	if c.BaseDir == "" {
		return fmt.Errorf("base_dir is required")
	}

	if c.SocketTimeout <= 0 || c.SocketTimeout > 300 {
		return fmt.Errorf("socket_timeout must be between 1 and 300 seconds")
	}

	if c.ShutdownTimeout <= 0 || c.ShutdownTimeout > 300 {
		return fmt.Errorf("shutdown_timeout must be between 1 and 300 seconds")
	}

	return nil
}

// VMDir returns the directory for a specific VM.
func (c Config) VMDir(vmID string) string {
	return filepath.Join(c.BaseDir, vmID)
}

// SocketPath returns the API socket path for a specific VM.
func (c Config) SocketPath(vmID string) string {
	return filepath.Join(c.VMDir(vmID), "api.sock")
}

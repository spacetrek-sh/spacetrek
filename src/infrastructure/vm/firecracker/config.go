// Package firecracker provides Firecracker configuration.
package firecracker

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
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

	// Execute command over vsock
	ExecEnabled        bool          `json:"exec_enabled" env:"FIRECRACKER_EXEC_ENABLED"`
	GuestAgentPort     uint32        `json:"guest_agent_port" env:"FIRECRACKER_GUEST_AGENT_PORT"`
	VsockSocketName    string        `json:"vsock_socket_name" env:"FIRECRACKER_VSOCK_SOCKET_NAME"`
	CIDMin             uint32        `json:"cid_min" env:"FIRECRACKER_CID_MIN"`
	CIDMax             uint32        `json:"cid_max" env:"FIRECRACKER_CID_MAX"`
	DefaultExecTimeout time.Duration `json:"default_exec_timeout" env:"FIRECRACKER_DEFAULT_EXEC_TIMEOUT"`
	MaxStdoutBytes     int           `json:"max_stdout_bytes" env:"FIRECRACKER_MAX_STDOUT_BYTES"`
	MaxStderrBytes     int           `json:"max_stderr_bytes" env:"FIRECRACKER_MAX_STDERR_BYTES"`

	// Network configuration. BridgeName must be non-empty to enable networking.
	Network NetworkConfig
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
		BinaryPath:         "firecracker",
		KernelPath:         kernelPath,
		BaseDir:            baseDir,
		KernelArgs:         "console=ttyS0 reboot=k panic=1 pci=off rw",
		MacAddress:         "02:FC:00:00:00:01",
		SocketTimeout:      30,
		ShutdownTimeout:    10,
		SMT:                false,
		EnableMmds:         true,
		ExecEnabled:        false,
		GuestAgentPort:     10789,
		VsockSocketName:    "agent.vsock",
		CIDMin:             1024,
		CIDMax:             65535,
		DefaultExecTimeout: 60 * time.Second,
		MaxStdoutBytes:     5 * 1024 * 1024,
		MaxStderrBytes:     5 * 1024 * 1024,
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

	if c.VsockSocketName == "" {
		return fmt.Errorf("vsock_socket_name is required")
	}

	if c.GuestAgentPort == 0 || c.GuestAgentPort > 65535 {
		return fmt.Errorf("guest_agent_port must be between 1 and 65535")
	}

	if c.CIDMin < 3 {
		return fmt.Errorf("cid_min must be >= 3")
	}

	if c.CIDMax < c.CIDMin {
		return fmt.Errorf("cid_max must be >= cid_min")
	}

	if c.DefaultExecTimeout < 1*time.Second || c.DefaultExecTimeout > 15*time.Minute {
		return fmt.Errorf("default_exec_timeout must be between 1s and 15m")
	}

	if c.MaxStdoutBytes < 1024 || c.MaxStdoutBytes > 32*1024*1024 {
		return fmt.Errorf("max_stdout_bytes must be between 1024 and 33554432")
	}

	if c.MaxStderrBytes < 1024 || c.MaxStderrBytes > 32*1024*1024 {
		return fmt.Errorf("max_stderr_bytes must be between 1024 and 33554432")
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

// VsockPath returns the host UDS path used for vsock for a specific VM.
func (c Config) VsockPath(vmID string) string {
	name := c.VsockSocketName
	if name == "" {
		name = "agent.vsock"
	}
	return filepath.Join(c.VMDir(vmID), name)
}

// SnapshotDir returns the base snapshots directory for a specific VM.
func (c Config) SnapshotDir(vmID string) string {
	return filepath.Join(c.VMDir(vmID), "snapshots")
}

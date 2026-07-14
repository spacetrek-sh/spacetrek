// Package vm defines runtime VM configuration types.
// Static resource configuration is now in the Environment domain.
package vm

import "time"

// RuntimeConfig represents runtime configuration for VM operations.
// Resource limits (VCPU, memory, disk) are defined in Environment.
type RuntimeConfig struct {
	// Network configuration (disabled by default for security)
	Network NetworkConfig `json:"network"`

	// Vsock configuration for host-guest communication.
	Vsock VsockConfig `json:"vsock"`

	// Command execution limits applied by provider/guest agent.
	Exec ExecLimits `json:"exec"`

	// Resource limits
	Timeout          time.Duration `json:"timeout"`        // Max execution duration
	MaxTasks         int           `json:"max_tasks"`      // Max concurrent tasks
	EnableNetworking bool          `json:"enable_network"` // Allow network access

	// Security options
	EnableSeccomp bool `json:"enable_seccomp"` // Enable syscall filtering
	EnableDebug   bool `json:"enable_debug"`   // Enable debug mode (dev only)

	// Snapshot options
	EnableDiffSnapshots bool `json:"enable_diff_snapshots"` // Enable incremental (dirty-page) memory + dm-snapshot disk diffs
}

// VsockConfig defines vsock settings for a VM.
type VsockConfig struct {
	Enabled bool `json:"enabled"` // Enable vsock device
	// GuestCID is optional. If zero, provider allocates deterministic CID.
	GuestCID uint32 `json:"guest_cid"`
	// GuestPort is the guest agent listen port.
	GuestPort uint32 `json:"guest_port"`
	// HostUDSPath is optional. If empty, provider derives VM-local socket path.
	HostUDSPath string `json:"host_uds_path"`
}

// ExecLimits defines command execution timeout and output caps.
type ExecLimits struct {
	Timeout        time.Duration `json:"timeout"`
	MaxStdoutBytes int           `json:"max_stdout_bytes"`
	MaxStderrBytes int           `json:"max_stderr_bytes"`
}

// NetworkConfig defines network settings for a VM.
type NetworkConfig struct {
	Enabled       bool   `json:"enabled"`        // Enable network interface
	Interface     string `json:"interface"`      // Network interface name
	IP            string `json:"ip"`             // IP address (for static config)
	Bridge        string `json:"bridge"`         // Bridge name
	AllowInternet bool   `json:"allow_internet"` // Allow internet access
}

// DefaultRuntimeConfig returns a sensible default runtime configuration.
func DefaultRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		Network: NetworkConfig{Enabled: false},
		Vsock: VsockConfig{
			Enabled: false,
		},
		Exec: ExecLimits{
			Timeout:        60 * time.Second,
			MaxStdoutBytes: 5 * 1024 * 1024,
			MaxStderrBytes: 5 * 1024 * 1024,
		},
		Timeout:          5 * time.Minute,
		MaxTasks:         10,
		EnableNetworking: false,
		EnableSeccomp:    true,
		EnableDebug:      false,
	}
}

// Validate checks if the runtime configuration is valid.
func (c RuntimeConfig) Validate() error {
	if c.Timeout < 1*time.Minute || c.Timeout > 60*time.Minute {
		return &ConfigError{Field: "timeout", Message: "must be between 1m and 60m"}
	}
	if c.MaxTasks < 1 || c.MaxTasks > 1000 {
		return &ConfigError{Field: "max_tasks", Message: "must be between 1 and 1000"}
	}
	if c.Vsock.GuestCID > 0 && c.Vsock.GuestCID < 3 {
		return &ConfigError{Field: "vsock.guest_cid", Message: "must be 0 (auto) or >= 3"}
	}
	if c.Vsock.GuestPort == 0 || c.Vsock.GuestPort > 65535 {
		return &ConfigError{Field: "vsock.guest_port", Message: "must be between 1 and 65535"}
	}
	if c.Exec.Timeout < 1*time.Second || c.Exec.Timeout > 15*time.Minute {
		return &ConfigError{Field: "exec.timeout", Message: "must be between 1s and 15m"}
	}
	if c.Exec.MaxStdoutBytes < 1024 || c.Exec.MaxStdoutBytes > 32*1024*1024 {
		return &ConfigError{Field: "exec.max_stdout_bytes", Message: "must be between 1024 and 33554432"}
	}
	if c.Exec.MaxStderrBytes < 1024 || c.Exec.MaxStderrBytes > 32*1024*1024 {
		return &ConfigError{Field: "exec.max_stderr_bytes", Message: "must be between 1024 and 33554432"}
	}
	return nil
}

// ConfigError represents a configuration validation error.
type ConfigError struct {
	Field   string
	Message string
}

func (e *ConfigError) Error() string {
	return e.Field + ": " + e.Message
}

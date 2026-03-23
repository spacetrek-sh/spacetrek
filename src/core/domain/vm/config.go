// Package vm defines runtime VM configuration types.
// Static resource configuration is now in the Environment domain.
package vm

import "time"

// RuntimeConfig represents runtime configuration for VM operations.
// Resource limits (VCPU, memory, disk) are defined in Environment.
type RuntimeConfig struct {
	// Network configuration (disabled by default for security)
	Network NetworkConfig `json:"network"`

	// Resource limits
	Timeout          time.Duration `json:"timeout"`       // Max execution duration
	MaxTasks         int           `json:"max_tasks"`     // Max concurrent tasks
	EnableNetworking bool          `json:"enable_network"` // Allow network access

	// Security options
	EnableSeccomp bool `json:"enable_seccomp"` // Enable syscall filtering
	EnableDebug   bool `json:"enable_debug"`   // Enable debug mode (dev only)
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
		Timeout: 5 * time.Minute,
		MaxTasks: 10,
		EnableNetworking: false,
		EnableSeccomp: true,
		EnableDebug: false,
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

// Package vm defines the VM provider interface for different VM backends.
package vm

import (
	"context"

	"github.com/kumori-sh/spacetrk/src/core/domain/environment"
)

// CreateSpec combines environment and runtime configuration for VM creation.
type CreateSpec struct {
	// InstanceID is the orchestrator-side VM identity and must be unique per runtime.
	InstanceID    string
	EnvironmentID string
	ImagePath     string
	Resources     environment.ResourceLimits
	Runtime       RuntimeConfig
}

// Backend defines the interface for VM backend providers.
// Different providers (Firecracker, Cloud Hypervisor) implement this interface.
type Backend interface {
	// Create creates and starts a new VM instance with the given specification.
	// Returns the VM ID or an error.
	Create(ctx context.Context, spec CreateSpec) (id string, err error)

	// Start resumes a stopped VM.
	Start(ctx context.Context, id string) error

	// Stop gracefully shuts down a VM.
	Stop(ctx context.Context, id string) error

	// Destroy forcefully terminates and removes a VM.
	Destroy(ctx context.Context, id string) error

	// Status returns the current runtime status of the VM.
	Status(ctx context.Context, id string) (RuntimeStatus, error)

	// Execute runs a command inside the VM and returns the output.
	Execute(ctx context.Context, id string, cmd []string) (stdout, stderr string, exitCode int, err error)

	// GetMetrics returns resource usage metrics for the VM.
	GetMetrics(ctx context.Context, id string) (Metrics, error)

	// CreateSnapshot pauses the VM, creates a full snapshot, and resumes the VM.
	// Returns the snapshot directory path and combined file size.
	CreateSnapshot(ctx context.Context, id string) (snapshotDir string, sizeBytes int64, err error)

	// RestoreFromSnapshot creates a new VM process from previously taken snapshot files.
	// The rootfs must already exist at the path from the original CreateSpec.
	RestoreFromSnapshot(ctx context.Context, spec CreateSpec, snapshotDir string) (id string, err error)

	// StopPreserving stops the VM process but preserves rootfs and snapshot files on disk.
	StopPreserving(ctx context.Context, id string) error
}

// RuntimeStatus represents the actual runtime status of a VM from the provider.
type RuntimeStatus struct {
	ID        string `json:"id"`
	State     string `json:"state"`      // "running", "stopped", "paused"
	PID       int    `json:"pid"`        // Process ID (if applicable)
	VsockPath string `json:"vsock_path"` // Host-side unix socket path for vsock
	GuestCID  uint32 `json:"guest_cid"`  // Guest CID allocated to the vsock device
	VCPU      int    `json:"vcpu"`       // Actual vCPUs assigned
	MemoryMB  int    `json:"memory_mb"`  // Actual memory in MB
	UptimeSec int    `json:"uptime_sec"` // Uptime in seconds
}

// Metrics represents resource usage metrics for a VM.
type Metrics struct {
	// CPU metrics
	CPUUsagePercent float64 `json:"cpu_usage_percent"`

	// Memory metrics
	MemoryUsedMB  int     `json:"memory_used_mb"`
	MemoryLimitMB int     `json:"memory_limit_mb"`
	MemoryPercent float64 `json:"memory_percent"`

	// Disk metrics
	DiskUsedMB  int     `json:"disk_used_mb"`
	DiskLimitMB int     `json:"disk_limit_mb"`
	DiskPercent float64 `json:"disk_percent"`

	// Network metrics (if enabled)
	NetworkBytesSent     int64 `json:"network_bytes_sent"`
	NetworkBytesReceived int64 `json:"network_bytes_received"`

	// Task metrics
	TasksExecuted int `json:"tasks_executed"`
	TasksFailed   int `json:"tasks_failed"`

	// Timestamp
	CollectedAt int64 `json:"collected_at"` // Unix timestamp
}

// Package vm defines the VM provider interface for different VM backends.
package vm

import (
	"context"

	"github.com/spacetrek-sh/spacetrek/src/core/domain/environment"
)

// CreateSpec combines environment and runtime configuration for VM creation.
type CreateSpec struct {
	// InstanceID is the orchestrator-side VM identity and must be unique per runtime.
	InstanceID    string
	EnvironmentID string
	ImagePath     string
	Resources     environment.ResourceLimits
	Workspace     WorkspaceConfig
	Runtime       RuntimeConfig
	// RestoreAsFull indicates the snapshot memory is a fully merged file, not a diff.
	// When true, Firecracker loads it as a full snapshot but still enables dirty-page
	// tracking (TrackDirtyPages) for future diff snapshots.
	RestoreAsFull bool

	// CowChainPaths holds the ordered list of cow files forming the snapshot
	// chain (full cow first, then each incremental cow). When non-empty,
	// the provider creates stacked dm-snapshot devices to reconstruct the
	// complete accumulated disk state at restore time.
	CowChainPaths []string

	// DiskImagePath is a self-contained full disk image from a flattened snapshot.
	// When set, the provider uses this as the rootfs base with a fresh CoW on top,
	// bypassing CoW chain reconstruction entirely. This is the new snapshot format
	// produced after the flatten fix — each snapshot is independently restorable.
	DiskImagePath string
}

// WorkspaceConfig captures persistent workspace provisioning for a VM.
type WorkspaceConfig struct {
	ConversationID string
	SizeGB         int
}

// SnapshotResult contains metadata about a created snapshot.
type SnapshotResult struct {
	SnapshotDir     string
	MemoryBytes     int64
	CowBytes        int64
	DiskBytes       int64
	PauseDurationMs int64
}

// SnapshotOptions controls snapshot creation behavior.
type SnapshotOptions struct {
	FullDisk bool // true = run flattenDmDevice, false = cow copy only
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

	// CreateSnapshot pauses the VM, creates a snapshot, and resumes the VM.
	// opts controls whether a full disk flatten is performed or only a cow copy.
	// Returns detailed result about the snapshot files and pause duration.
	CreateSnapshot(ctx context.Context, id string, opts SnapshotOptions) (*SnapshotResult, error)

	// RestoreFromSnapshot creates a new VM process from previously taken snapshot files.
	// The rootfs must already exist at the path from the original CreateSpec.
	RestoreFromSnapshot(ctx context.Context, spec CreateSpec, snapshotDir string) (id string, err error)

	// StopPreserving stops the VM process but preserves rootfs and snapshot files on disk.
	StopPreserving(ctx context.Context, id string) error

	// ReadFile reads a file from the guest VM, returning content in cat -n format.
	ReadFile(ctx context.Context, id string, path string, offset, limit int) (string, error)

	// WriteFile writes content to a file in the guest VM, creating it if needed.
	WriteFile(ctx context.Context, id string, path string, content string, mode int) error

	// EditFile performs a surgical string replacement on a file in the guest VM.
	EditFile(ctx context.Context, id string, path string, oldString, newString string, replaceAll bool) error
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

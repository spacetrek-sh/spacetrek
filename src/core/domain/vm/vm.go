// Package vm defines the VM domain entity and related types.
package vm

import (
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/spacetrek-sh/spacetrek/src/core/domain/vm/naming"
)

// Status represents the lifecycle state of a VM.
type Status string

const (
	StatusProvisioning Status = "provisioning"
	StatusReady        Status = "ready"
	StatusRunning      Status = "running"
	StatusIdle         Status = "idle"
	StatusTerminated   Status = "terminated"
)

// Provider represents the VM backend provider.
type Provider string

const (
	ProviderFirecracker     Provider = "firecracker"
	ProviderCloudHypervisor Provider = "cloud-hypervisor"
)

// VM represents a microVM instance for secure task execution.
// Aligned with database table vm_instances.
type VM struct {
	ID              string   `db:"id"`
	Name            string   `db:"name"` // DNS label, unique, human-readable
	EnvironmentID   string   `db:"environment_id"` // FK to environments
	ConversationID  string   `db:"conversation_id"`
	Provider        Provider `db:"provider"`
	Status          Status   `db:"status"`
	WorkspaceSizeGB int      `db:"workspace_size_gb"`

	// Runtime metadata (nullable, persisted for reconciliation)
	RuntimeID       *string    `db:"runtime_id"`
	SocketPath      *string    `db:"socket_path"`
	VsockPath       *string    `db:"vsock_path"`
	GuestCID        *uint32    `db:"guest_cid"`
	PID             *int       `db:"pid"`
	RuntimeState    *string    `db:"runtime_state_source"`
	LastHeartbeatAt *time.Time `db:"last_heartbeat_at"`
	IdleDeadlineAt  *time.Time `db:"idle_deadline_at"`

	// Resource overrides (NULL = use environment default)
	VCPU     *int `db:"vcpu"`      // Optional vCPU override
	MemoryMB *int `db:"memory_mb"` // Optional memory override in MB
	DiskMB   *int `db:"disk_mb"`   // Optional disk size override in MB

	// Network
	IPAddress *string `db:"ip_address"` // Assigned IP (nullable)

	// Session binding (mapped from chat_id in DB)
	ChatID     *string    `db:"chat_id"`     // Bound chat (nullable)
	AssignedAt *time.Time `db:"assigned_at"` // When VM was assigned

	// Resume tracking
	LastResumedAt *time.Time `db:"last_resumed_at"` // When VM was last resumed from snapshot

	// Snapshot configuration
	DiffSnapshotsEnabled bool `db:"diff_snapshots_enabled"`

	// Lifecycle
	TerminatedAt *time.Time `db:"terminated_at"` // When VM was terminated
	CreatedAt    time.Time  `db:"created_at"`
}

// CreateParams contains the parameters for creating a new VM.
type CreateParams struct {
	EnvironmentID   string
	ConversationID  string
	Provider        Provider
	WorkspaceSizeGB int
	// Name is optional. nil = generate a random Docker-style name.
	// Non-nil is normalized via NormalizeName; the caller is responsible
	// for rejecting an empty post-normalization result.
	Name *string
	// Optional resource overrides
	VCPU     *int // nil = use environment default
	MemoryMB *int // nil = use environment default
	DiskMB   *int // nil = use environment default
}

// New creates a new VM entity with a generated ID and timestamp.
func New(params CreateParams) *VM {
	now := time.Now().UTC()

	// Apply defaults
	provider := params.Provider
	if provider == "" {
		provider = ProviderFirecracker
	}

	workspaceSizeGB := params.WorkspaceSizeGB
	if workspaceSizeGB <= 0 {
		workspaceSizeGB = 2
	}

	return &VM{
		ID:              uuid.NewString(),
		Name:            resolveName(params.Name),
		EnvironmentID:   params.EnvironmentID,
		ConversationID:  params.ConversationID,
		Provider:        provider,
		Status:          StatusProvisioning,
		WorkspaceSizeGB: workspaceSizeGB,
		VCPU:            params.VCPU,
		MemoryMB:        params.MemoryMB,
		DiskMB:          params.DiskMB,
		CreatedAt:       now,
	}
}

// resolveName picks the initial VM name: a normalized user-provided name, or a
// random Docker-style name if none was supplied. An empty result means the
// user-provided name normalized to nothing; callers reject that case.
func resolveName(provided *string) string {
	if provided == nil {
		return naming.Generate()
	}
	return NormalizeName(*provided)
}

var (
	nonNameChar    = regexp.MustCompile(`[^a-z0-9-]+`)
	consecutiveDsh = regexp.MustCompile(`-{2,}`)
)

// NormalizeName lowercases, replaces any non-[a-z0-9-] rune with a hyphen,
// collapses consecutive hyphens, trims leading/trailing hyphens, and truncates
// to 63 chars (DNS label limit per RFC 1123). Returns "" for input that
// produces an empty label (e.g. "!!!", "   "). Callers decide whether empty is
// an error.
func NormalizeName(s string) string {
	s = strings.ToLower(s)
	s = nonNameChar.ReplaceAllString(s, "-")
	s = consecutiveDsh.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > naming.MaxLen {
		s = s[:naming.MaxLen]
		s = strings.TrimRight(s, "-")
	}
	return s
}

// HasCustomResources returns true if the VM has custom resource overrides.
func (v *VM) HasCustomResources() bool {
	return v.VCPU != nil || v.MemoryMB != nil || v.DiskMB != nil
}

// GetVCPU returns the effective vCPU count (override or default).
func (v *VM) GetVCPU(defaultVCPU int) int {
	if v.VCPU != nil && *v.VCPU > 0 {
		return *v.VCPU
	}
	return defaultVCPU
}

// GetMemoryMB returns the effective memory in MB (override or default).
func (v *VM) GetMemoryMB(defaultMB int) int {
	if v.MemoryMB != nil && *v.MemoryMB > 0 {
		return *v.MemoryMB
	}
	return defaultMB
}

// GetDiskMB returns the effective disk size in MB (override or default).
func (v *VM) GetDiskMB(defaultMB int) int {
	if v.DiskMB != nil && *v.DiskMB > 0 {
		return *v.DiskMB
	}
	return defaultMB
}

// IsAvailable checks if the VM is available for chat assignment.
// A VM in Ready (fresh from pool), Running (restored from snapshot), or Idle
// (stopped but recoverable) state with no chat assignment can be assigned.
func (v *VM) IsAvailable() bool {
	return (v.Status == StatusReady || v.Status == StatusRunning || v.Status == StatusIdle) && v.ChatID == nil
}

// AssignTo assigns the VM to a chat.
func (v *VM) AssignTo(chatID string) {
	now := time.Now().UTC()
	v.ChatID = &chatID
	v.AssignedAt = &now
	v.Status = StatusRunning
}

// Unassign releases the VM from the current chat.
func (v *VM) Unassign() {
	v.ChatID = nil
	v.AssignedAt = nil
	v.Status = StatusIdle
}

// Terminate marks the VM as terminated.
func (v *VM) Terminate() {
	now := time.Now().UTC()
	v.Status = StatusTerminated
	v.TerminatedAt = &now
	v.ChatID = nil // Release chat on terminate
}

// IsTerminated checks if the VM is terminated.
func (v *VM) IsTerminated() bool {
	return v.Status == StatusTerminated
}

// GetAssignedChatID returns the assigned chat ID if any.
func (v *VM) GetAssignedChatID() *string {
	return v.ChatID
}

// SetRuntimeMetadata stores provider runtime metadata for reconciliation.
func (v *VM) SetRuntimeMetadata(runtimeID, socketPath string, pid int, state string) {
	now := time.Now().UTC()
	if runtimeID != "" {
		v.RuntimeID = &runtimeID
	}
	if socketPath != "" {
		v.SocketPath = &socketPath
	}
	if state != "" {
		v.RuntimeState = &state
	}
	v.LastHeartbeatAt = &now
	if pid > 0 {
		v.PID = &pid
	}
}

// SetRuntimeVsockMetadata stores provider vsock metadata used for guest agent execution.
func (v *VM) SetRuntimeVsockMetadata(vsockPath string, guestCID uint32) {
	if vsockPath != "" {
		v.VsockPath = &vsockPath
	}
	if guestCID > 0 {
		v.GuestCID = &guestCID
	}
}

// ClearRuntimeVsockMetadata releases provider vsock metadata for CID reuse.
func (v *VM) ClearRuntimeVsockMetadata() {
	v.VsockPath = nil
	v.GuestCID = nil
}

// MarkResumed records that this VM was resumed from a snapshot.
func (v *VM) MarkResumed() {
	now := time.Now().UTC()
	v.LastResumedAt = &now
}

// IsRecentlyResumed returns true if the VM was resumed within the given grace period.
func (v *VM) IsRecentlyResumed(gracePeriod time.Duration) bool {
	if v.LastResumedAt == nil {
		return false
	}
	return time.Since(*v.LastResumedAt) < gracePeriod
}

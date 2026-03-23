// Package vm defines the VM domain entity and related types.
package vm

import (
	"time"

	"github.com/google/uuid"
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
	ID            string     `db:"id"`
	EnvironmentID string     `db:"environment_id"` // FK to environments
	Provider      Provider   `db:"provider"`
	Status        Status     `db:"status"`

	// Resource overrides (NULL = use environment default)
	VCPU      *int `db:"vcpu"`       // Optional vCPU override
	MemoryMB  *int `db:"memory_mb"`  // Optional memory override in MB
	DiskMB    *int `db:"disk_mb"`    // Optional disk size override in MB

	// Network
	IPAddress *string `db:"ip_address"` // Assigned IP (nullable)

	// Session binding (mapped from chat_id in DB)
	ChatID     *string    `db:"chat_id"` // Bound chat/session (nullable)
	AssignedAt *time.Time `db:"assigned_at"` // When VM was assigned

	// Lifecycle
	TerminatedAt *time.Time `db:"terminated_at"` // When VM was terminated
	CreatedAt    time.Time  `db:"created_at"`
}

// CreateParams contains the parameters for creating a new VM.
type CreateParams struct {
	EnvironmentID string
	Provider      Provider
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

	return &VM{
		ID:            uuid.NewString(),
		EnvironmentID: params.EnvironmentID,
		Provider:      provider,
		Status:        StatusProvisioning,
		VCPU:          params.VCPU,
		MemoryMB:      params.MemoryMB,
		DiskMB:        params.DiskMB,
		CreatedAt:     now,
	}
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
func (v *VM) IsAvailable() bool {
	return v.Status == StatusReady && v.ChatID == nil
}

// AssignTo assigns the VM to a chat/session.
func (v *VM) AssignTo(chatID string) {
	now := time.Now().UTC()
	v.ChatID = &chatID
	v.AssignedAt = &now
	v.Status = StatusRunning
}

// Unassign releases the VM from the current chat/session.
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

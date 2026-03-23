// Package vm provides HTTP request/response DTOs for VM endpoints.
package vm

// createVMRequest is the JSON body for POST /api/v1/vm.
type createVMRequest struct {
	EnvironmentID string `json:"environment_id" validate:"required"`
	Provider      string `json:"provider" validate:"omitempty,oneof=firecracker cloud-hypervisor"`
	// Optional resource overrides (null = use environment default)
	VCPU     *int `json:"vcpu" validate:"omitempty,min=1,max=16"`
	MemoryMB *int `json:"memory_mb" validate:"omitempty,min=128,max=32768"`
	DiskMB   *int `json:"disk_mb" validate:"omitempty,min=512,max=102400"`
}

// createVMResponse is the JSON response for successful VM creation.
type createVMResponse struct {
	ID            string  `json:"id"`
	EnvironmentID string  `json:"environment_id"`
	Provider      string  `json:"provider"`
	Status        string  `json:"status"`
	// Effective resources (what was actually provisioned)
	VCPU     int    `json:"vcpu"`
	MemoryMB int    `json:"memory_mb"`
	DiskMB   int    `json:"disk_mb"`
}

// getVMResponse is the JSON response for GET /api/v1/vm/{id}.
type getVMResponse struct {
	ID            string  `json:"id"`
	EnvironmentID string  `json:"environment_id"`
	Provider      string  `json:"provider"`
	Status        string  `json:"status"`
	// Resource configuration
	VCPU         *int    `json:"vcpu,omitempty"`          // nil = using environment default
	MemoryMB     *int    `json:"memory_mb,omitempty"`     // nil = using environment default
	DiskMB       *int    `json:"disk_mb,omitempty"`       // nil = using environment default
	HasOverrides bool    `json:"has_overrides"`           // true if any override is set
	// Network
	IPAddress *string `json:"ip_address,omitempty"`
	// Session binding
	ChatID     *string `json:"chat_id,omitempty"`
	AssignedAt *string `json:"assigned_at,omitempty"`
	// Timestamps
	CreatedAt    string  `json:"created_at"`
	TerminatedAt *string `json:"terminated_at,omitempty"`
}

// deleteVMResponse is the JSON response for successful VM deletion.
type deleteVMResponse struct {
	ID string `json:"id"`
}

// executeCommandRequest is the JSON body for POST /api/v1/vm/{id}/execute.
type executeCommandRequest struct {
	Command string `json:"command" validate:"required"`
}

// executeCommandResponse is the JSON response for successful command execution.
type executeCommandResponse struct {
	Output string `json:"output"`
}

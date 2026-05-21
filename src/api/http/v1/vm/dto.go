// Package vm provides HTTP request/response DTOs for VM endpoints.
package vm

// createVMRequest is the JSON body for POST /api/v1/vm.
type createVMRequest struct {
	EnvironmentID   string `json:"environment_id" validate:"required"`
	ConversationID  string `json:"conversation_id" validate:"required"`
	Provider        string `json:"provider" validate:"omitempty,oneof=firecracker cloud-hypervisor"`
	WorkspaceSizeGB int    `json:"workspace_size_gb,omitempty" validate:"omitempty,min=1,max=64"`
	// Optional resource overrides (null = use environment default)
	VCPU     *int `json:"vcpu" validate:"omitempty,min=1,max=16"`
	MemoryMB *int `json:"memory_mb" validate:"omitempty,min=128,max=32768"`
	DiskMB   *int `json:"disk_mb" validate:"omitempty,min=512,max=102400"`
}

// createVMResponse is the JSON response for successful VM creation.
type createVMResponse struct {
	ID              string  `json:"id"`
	EnvironmentID   string  `json:"environment_id"`
	ConversationID  string  `json:"conversation_id"`
	Provider        string  `json:"provider"`
	Status          string  `json:"status"`
	WorkspaceSizeGB int     `json:"workspace_size_gb"`
	RuntimeID       *string `json:"runtime_id,omitempty"`
	RuntimeState    *string `json:"runtime_state,omitempty"`
	PID             *int    `json:"pid,omitempty"`
	LastHeartbeatAt *string `json:"last_heartbeat_at,omitempty"`
	IdleDeadlineAt  *string `json:"idle_deadline_at,omitempty"`
	// Effective resources (what was actually provisioned)
	VCPU     int `json:"vcpu"`
	MemoryMB int `json:"memory_mb"`
	DiskMB   int `json:"disk_mb"`
}

// getVMResponse is the JSON response for GET /api/v1/vm/{id}.
type getVMResponse struct {
	ID              string `json:"id"`
	EnvironmentID   string `json:"environment_id"`
	ConversationID  string `json:"conversation_id"`
	Provider        string `json:"provider"`
	Status          string `json:"status"`
	WorkspaceSizeGB int    `json:"workspace_size_gb"`
	// Runtime observed metadata
	RuntimeID       *string `json:"runtime_id,omitempty"`
	RuntimeState    *string `json:"runtime_state,omitempty"`
	PID             *int    `json:"pid,omitempty"`
	LastHeartbeatAt *string `json:"last_heartbeat_at,omitempty"`
	IdleDeadlineAt  *string `json:"idle_deadline_at,omitempty"`
	// Resource configuration
	VCPU         *int `json:"vcpu,omitempty"`      // nil = using environment default
	MemoryMB     *int `json:"memory_mb,omitempty"` // nil = using environment default
	DiskMB       *int `json:"disk_mb,omitempty"`   // nil = using environment default
	HasOverrides bool `json:"has_overrides"`       // true if any override is set
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

// executeCommandResponse is the JSON response for command execution.
type executeCommandResponse struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// assignVMRequest is the JSON body for POST /api/v1/vm/{id}/assign.
type assignVMRequest struct {
	ChatID string `json:"chat_id" validate:"required"`
}

// vmLeaseResponse represents one active VM lease.
type vmLeaseResponse struct {
	ID       string `json:"id"`
	ChatID   string `json:"chat_id"`
	VMID     string `json:"vm_id"`
	LeasedAt string `json:"leased_at"`
}

// runtimeSnapshotResponse represents runtime-observed VM state for monitoring streams.
type runtimeSnapshotResponse struct {
	ID                   string  `json:"id"`
	EnvironmentID        string  `json:"environment_id"`
	Provider             string  `json:"provider"`
	Status               string  `json:"status"`
	RuntimeID            *string `json:"runtime_id,omitempty"`
	RuntimeState         *string `json:"runtime_state,omitempty"`
	PID                  *int    `json:"pid,omitempty"`
	LastHeartbeatAt      *string `json:"last_heartbeat_at,omitempty"`
	IdleDeadlineAt       *string `json:"idle_deadline_at,omitempty"`
	ChatID               *string `json:"chat_id,omitempty"`
	CPUUsagePercent      float64 `json:"cpu_usage_percent"`
	MemoryUsedMB         int     `json:"memory_used_mb"`
	MemoryLimitMB        int     `json:"memory_limit_mb"`
	MemoryPercent        float64 `json:"memory_percent"`
	DiskUsedMB           int     `json:"disk_used_mb"`
	DiskLimitMB          int     `json:"disk_limit_mb"`
	NetworkBytesSent     int64   `json:"network_bytes_sent"`
	NetworkBytesReceived int64   `json:"network_bytes_received"`
	CollectedAt          int64   `json:"collected_at"`
}

// vmMetricsResponse returns realtime resource usage for one VM runtime.
type vmMetricsResponse struct {
	VMID                 string  `json:"vm_id"`
	CPUUsagePercent      float64 `json:"cpu_usage_percent"`
	MemoryUsedMB         int     `json:"memory_used_mb"`
	MemoryLimitMB        int     `json:"memory_limit_mb"`
	MemoryPercent        float64 `json:"memory_percent"`
	DiskUsedMB           int     `json:"disk_used_mb"`
	DiskLimitMB          int     `json:"disk_limit_mb"`
	DiskPercent          float64 `json:"disk_percent"`
	NetworkBytesSent     int64   `json:"network_bytes_sent"`
	NetworkBytesReceived int64   `json:"network_bytes_received"`
	CollectedAt          int64   `json:"collected_at"`
}

// vmMetricsHistoryPointResponse is one historical metrics point.
type vmMetricsHistoryPointResponse struct {
	CPUUsagePercent      float64 `json:"cpu_usage_percent"`
	MemoryUsedMB         int     `json:"memory_used_mb"`
	MemoryLimitMB        int     `json:"memory_limit_mb"`
	MemoryPercent        float64 `json:"memory_percent"`
	DiskUsedMB           int     `json:"disk_used_mb"`
	DiskLimitMB          int     `json:"disk_limit_mb"`
	DiskPercent          float64 `json:"disk_percent"`
	NetworkBytesSent     int64   `json:"network_bytes_sent"`
	NetworkBytesReceived int64   `json:"network_bytes_received"`
	CollectedAt          int64   `json:"collected_at"`
}

// vmMetricsHistoryResponse returns historical timeseries points for one VM.
type vmMetricsHistoryResponse struct {
	VMID   string                          `json:"vm_id"`
	Points []vmMetricsHistoryPointResponse `json:"points"`
}

// vmSnapshotResponse is the JSON response for POST /api/v1/vm/{id}/snapshot.
type vmSnapshotResponse struct {
	ID        string `json:"id"`
	VMID      string `json:"vm_id"`
	Type      string `json:"type"`
	SizeBytes int64  `json:"size_bytes"`
	CreatedAt string `json:"created_at"`
}

// resumeVMRequest is the JSON body for POST /api/v1/vm/resume.
type resumeVMRequest struct {
	ChatID string `json:"chat_id" validate:"required"`
}

// fleetVMResponse is a single VM in the fleet SSE stream.
type fleetVMResponse struct {
	ID      string  `json:"id"`
	Agent   string  `json:"agent,omitempty"`
	Uptime  string  `json:"uptime"`
	Mem     string  `json:"mem"`
	MemPct  float64 `json:"memPct"`
	CPU     string  `json:"cpu"`
	Disk    string  `json:"disk"`
	DiskPct float64 `json:"diskPct"`
	Status  string  `json:"status"`
	IP      string  `json:"ip,omitempty"`
	Created string  `json:"created"`
}

// activityEventResponse is a single event in the activity SSE stream.
type activityEventResponse struct {
	Time string `json:"time"`
	Type string `json:"type"`
	VM   string `json:"vm"`
	Msg  string `json:"msg"`
}

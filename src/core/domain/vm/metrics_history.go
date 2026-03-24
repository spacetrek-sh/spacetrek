package vm

import (
	"context"
	"time"
)

// MetricsPoint is a persisted timeseries sample for one VM.
type MetricsPoint struct {
	VMID                 string    `json:"vm_id"`
	CPUUsagePercent      float64   `json:"cpu_usage_percent"`
	MemoryUsedMB         int       `json:"memory_used_mb"`
	MemoryLimitMB        int       `json:"memory_limit_mb"`
	MemoryPercent        float64   `json:"memory_percent"`
	DiskUsedMB           int       `json:"disk_used_mb"`
	DiskLimitMB          int       `json:"disk_limit_mb"`
	DiskPercent          float64   `json:"disk_percent"`
	NetworkBytesSent     int64     `json:"network_bytes_sent"`
	NetworkBytesReceived int64     `json:"network_bytes_received"`
	CollectedAt          time.Time `json:"collected_at"`
}

// MetricsHistoryRepository defines persistence for VM metrics timeseries.
type MetricsHistoryRepository interface {
	Insert(ctx context.Context, point MetricsPoint) error
	ListByVM(ctx context.Context, vmID string, from, to *time.Time, limit int) ([]MetricsPoint, error)
}

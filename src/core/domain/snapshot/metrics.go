package snapshot

import (
	"context"
	"time"
)

// SnapshotMetrics captures performance data for thesis analysis.
type SnapshotMetrics struct {
	ID        int64     `db:"id" json:"id"`
	SnapshotID string   `db:"snapshot_id" json:"snapshot_id"`
	VMID      string    `db:"vm_id" json:"vm_id"`
	Type      Type      `db:"type" json:"type"`

	// Creation metrics
	PauseDurationMs  int64 `db:"pause_duration_ms" json:"pause_duration_ms"`
	MemoryBytes      int64 `db:"memory_bytes" json:"memory_bytes"`
	MemoryZstBytes   int64 `db:"memory_zst_bytes" json:"memory_zst_bytes"`
	CowBytes         int64 `db:"cow_bytes" json:"cow_bytes"`
	CowZstBytes      int64 `db:"cow_zst_bytes" json:"cow_zst_bytes"`
	UploadDurationMs int64 `db:"upload_duration_ms" json:"upload_duration_ms"`

	// Resume metrics
	DownloadDurationMs    int64 `db:"download_duration_ms" json:"download_duration_ms"`
	DecompressDurationMs  int64 `db:"decompress_duration_ms" json:"decompress_duration_ms"`
	RestoreDurationMs     int64 `db:"restore_duration_ms" json:"restore_duration_ms"`
	AgentReadyMs          int64 `db:"agent_ready_ms" json:"agent_ready_ms"`
	TotalResumeMs         int64 `db:"total_resume_ms" json:"total_resume_ms"`

	// Context
	GuestRAMMB   int    `db:"guest_ram_mb" json:"guest_ram_mb"`
	WorkloadType string `db:"workload_type" json:"workload_type"`
	ChainDepth   int    `db:"chain_depth" json:"chain_depth"`
	CreatedAt    time.Time `db:"created_at" json:"created_at"`
}

// MetricsRepository defines persistence for snapshot performance metrics.
type MetricsRepository interface {
	Insert(ctx context.Context, m *SnapshotMetrics) error
	ListByVM(ctx context.Context, vmID string, limit int) ([]SnapshotMetrics, error)
	ListByType(ctx context.Context, snapType Type, limit int) ([]SnapshotMetrics, error)
}

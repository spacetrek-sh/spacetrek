package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/snapshot"
)

type snapshotMetricsRepository struct {
	db *DB
}

type snapshotMetricsRow struct {
	ID                  int64  `db:"id"`
	SnapshotID          string `db:"snapshot_id"`
	VMID                string `db:"vm_id"`
	Type                string `db:"type"`
	PauseDurationMs     int64  `db:"pause_duration_ms"`
	MemoryBytes         int64  `db:"memory_bytes"`
	MemoryZstBytes      int64  `db:"memory_zst_bytes"`
	CowBytes            int64  `db:"cow_bytes"`
	CowZstBytes         int64  `db:"cow_zst_bytes"`
	DiskBytes           int64  `db:"disk_bytes"`
	UploadDurationMs    int64  `db:"upload_duration_ms"`
	DownloadDurationMs  int64  `db:"download_duration_ms"`
	DecompressDurationMs int64 `db:"decompress_duration_ms"`
	RestoreDurationMs   int64  `db:"restore_duration_ms"`
	AgentReadyMs        int64  `db:"agent_ready_ms"`
	TotalResumeMs       int64  `db:"total_resume_ms"`
	GuestRAMMB          int    `db:"guest_ram_mb"`
	WorkloadType        string `db:"workload_type"`
	ChainDepth          int    `db:"chain_depth"`
	CreatedAt           time.Time `db:"created_at"`
}

// NewSnapshotMetricsRepository creates a PostgreSQL-backed snapshot metrics repository.
func NewSnapshotMetricsRepository(db *DB) snapshot.MetricsRepository {
	return &snapshotMetricsRepository{db: db}
}

func (r *snapshotMetricsRepository) Insert(ctx context.Context, m *snapshot.SnapshotMetrics) error {
	logger := pkglog.FromContext(ctx)

	query := `
		INSERT INTO snapshot_metrics (
			snapshot_id, vm_id, type,
			pause_duration_ms, memory_bytes, memory_zst_bytes,
			cow_bytes, cow_zst_bytes, disk_bytes, upload_duration_ms,
			download_duration_ms, decompress_duration_ms,
			restore_duration_ms, agent_ready_ms, total_resume_ms,
			guest_ram_mb, workload_type, chain_depth
		) VALUES (
			$1, $2, $3,
			$4, $5, $6,
			$7, $8, $9, $10,
			$11, $12,
			$13, $14, $15,
			$16, $17, $18
		) RETURNING id
	`

	if err := r.db.QueryRowxContext(ctx, query,
		m.SnapshotID, m.VMID, string(m.Type),
		m.PauseDurationMs, m.MemoryBytes, m.MemoryZstBytes,
		m.CowBytes, m.CowZstBytes, m.DiskBytes, m.UploadDurationMs,
		m.DownloadDurationMs, m.DecompressDurationMs,
		m.RestoreDurationMs, m.AgentReadyMs, m.TotalResumeMs,
		m.GuestRAMMB, m.WorkloadType, m.ChainDepth,
	).Scan(&m.ID); err != nil {
		logger.ErrorContext(ctx, "postgres: insert snapshot metrics failed", "snapshot_id", m.SnapshotID, "error", err)
		return exception.Internal(fmt.Errorf("insert snapshot metrics: %w", err))
	}

	return nil
}

func (r *snapshotMetricsRepository) ListByVM(ctx context.Context, vmID string, limit int) ([]snapshot.SnapshotMetrics, error) {
	logger := pkglog.FromContext(ctx)

	if limit <= 0 {
		limit = 100
	}
	if limit > 5000 {
		limit = 5000
	}

	query := `
		SELECT id, snapshot_id, vm_id, type,
		       pause_duration_ms, memory_bytes, memory_zst_bytes,
		       cow_bytes, cow_zst_bytes, disk_bytes, upload_duration_ms,
		       download_duration_ms, decompress_duration_ms,
		       restore_duration_ms, agent_ready_ms, total_resume_ms,
		       guest_ram_mb, workload_type, chain_depth, created_at
		FROM snapshot_metrics
		WHERE vm_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`

	var rows []snapshotMetricsRow
	if err := r.db.SelectContext(ctx, &rows, query, vmID, limit); err != nil {
		logger.ErrorContext(ctx, "postgres: list snapshot metrics by vm failed", "vm_id", vmID, "error", err)
		return nil, exception.Internal(fmt.Errorf("list snapshot metrics by vm: %w", err))
	}

	out := make([]snapshot.SnapshotMetrics, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapSnapshotMetricsRow(row))
	}
	return out, nil
}

func (r *snapshotMetricsRepository) ListByType(ctx context.Context, snapType snapshot.Type, limit int) ([]snapshot.SnapshotMetrics, error) {
	logger := pkglog.FromContext(ctx)

	if limit <= 0 {
		limit = 100
	}
	if limit > 5000 {
		limit = 5000
	}

	query := `
		SELECT id, snapshot_id, vm_id, type,
		       pause_duration_ms, memory_bytes, memory_zst_bytes,
		       cow_bytes, cow_zst_bytes, disk_bytes, upload_duration_ms,
		       download_duration_ms, decompress_duration_ms,
		       restore_duration_ms, agent_ready_ms, total_resume_ms,
		       guest_ram_mb, workload_type, chain_depth, created_at
		FROM snapshot_metrics
		WHERE type = $1
		ORDER BY created_at DESC
		LIMIT $2
	`

	var rows []snapshotMetricsRow
	if err := r.db.SelectContext(ctx, &rows, query, string(snapType), limit); err != nil {
		logger.ErrorContext(ctx, "postgres: list snapshot metrics by type failed", "type", snapType, "error", err)
		return nil, exception.Internal(fmt.Errorf("list snapshot metrics by type: %w", err))
	}

	out := make([]snapshot.SnapshotMetrics, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapSnapshotMetricsRow(row))
	}
	return out, nil
}

func mapSnapshotMetricsRow(row snapshotMetricsRow) snapshot.SnapshotMetrics {
	return snapshot.SnapshotMetrics{
		ID:                   row.ID,
		SnapshotID:           row.SnapshotID,
		VMID:                 row.VMID,
		Type:                 snapshot.Type(row.Type),
		PauseDurationMs:      row.PauseDurationMs,
		MemoryBytes:          row.MemoryBytes,
		MemoryZstBytes:       row.MemoryZstBytes,
		CowBytes:             row.CowBytes,
		CowZstBytes:          row.CowZstBytes,
		DiskBytes:            row.DiskBytes,
		UploadDurationMs:     row.UploadDurationMs,
		DownloadDurationMs:   row.DownloadDurationMs,
		DecompressDurationMs: row.DecompressDurationMs,
		RestoreDurationMs:    row.RestoreDurationMs,
		AgentReadyMs:         row.AgentReadyMs,
		TotalResumeMs:        row.TotalResumeMs,
		GuestRAMMB:           row.GuestRAMMB,
		WorkloadType:         row.WorkloadType,
		ChainDepth:           row.ChainDepth,
		CreatedAt:            row.CreatedAt.UTC(),
	}
}

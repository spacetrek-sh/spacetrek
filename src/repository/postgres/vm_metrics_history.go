package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/kumori-sh/spacetrk/pkg/exception"
	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	vmdomain "github.com/kumori-sh/spacetrk/src/core/domain/vm"
)

type vmMetricsHistoryRepository struct {
	db *DB
}

type vmMetricsHistoryRow struct {
	VMID                 string    `db:"vm_id"`
	CPUUsagePercent      float64   `db:"cpu_usage_percent"`
	MemoryUsedMB         int       `db:"memory_used_mb"`
	MemoryLimitMB        int       `db:"memory_limit_mb"`
	MemoryPercent        float64   `db:"memory_percent"`
	DiskUsedMB           int       `db:"disk_used_mb"`
	DiskLimitMB          int       `db:"disk_limit_mb"`
	DiskPercent          float64   `db:"disk_percent"`
	NetworkBytesSent     int64     `db:"network_bytes_sent"`
	NetworkBytesReceived int64     `db:"network_bytes_received"`
	CollectedAt          time.Time `db:"collected_at"`
}

// NewVMMetricsHistoryRepository creates a PostgreSQL-backed metrics history repository.
func NewVMMetricsHistoryRepository(db *DB) vmdomain.MetricsHistoryRepository {
	return &vmMetricsHistoryRepository{db: db}
}

func (r *vmMetricsHistoryRepository) Insert(ctx context.Context, point vmdomain.MetricsPoint) error {
	logger := pkglog.FromContext(ctx)

	query := `
		INSERT INTO vm_metrics_history (
			vm_id, collected_at,
			cpu_usage_percent,
			memory_used_mb, memory_limit_mb, memory_percent,
			disk_used_mb, disk_limit_mb, disk_percent,
			network_bytes_sent, network_bytes_received
		)
		VALUES (
			$1, $2,
			$3,
			$4, $5, $6,
			$7, $8, $9,
			$10, $11
		)
	`

	if _, err := r.db.ExecContext(ctx, query,
		point.VMID, point.CollectedAt,
		point.CPUUsagePercent,
		point.MemoryUsedMB, point.MemoryLimitMB, point.MemoryPercent,
		point.DiskUsedMB, point.DiskLimitMB, point.DiskPercent,
		point.NetworkBytesSent, point.NetworkBytesReceived,
	); err != nil {
		logger.ErrorContext(ctx, "postgres: insert vm metrics history failed", "vm_id", point.VMID, "error", err)
		return exception.Internal(fmt.Errorf("insert vm metrics history: %w", err))
	}

	return nil
}

func (r *vmMetricsHistoryRepository) ListByVM(ctx context.Context, vmID string, from, to *time.Time, limit int) ([]vmdomain.MetricsPoint, error) {
	logger := pkglog.FromContext(ctx)

	if limit <= 0 {
		limit = 300
	}
	if limit > 5000 {
		limit = 5000
	}

	query := `
		SELECT vm_id,
		       cpu_usage_percent,
		       memory_used_mb, memory_limit_mb, memory_percent,
		       disk_used_mb, disk_limit_mb, disk_percent,
		       network_bytes_sent, network_bytes_received,
		       collected_at
		FROM vm_metrics_history
		WHERE vm_id = $1
		  AND ($2::timestamptz IS NULL OR collected_at >= $2)
		  AND ($3::timestamptz IS NULL OR collected_at <= $3)
		ORDER BY collected_at ASC
		LIMIT $4
	`

	rows := make([]vmMetricsHistoryRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query, vmID, from, to, limit); err != nil {
		logger.ErrorContext(ctx, "postgres: list vm metrics history failed", "vm_id", vmID, "error", err)
		return nil, exception.Internal(fmt.Errorf("list vm metrics history: %w", err))
	}

	out := make([]vmdomain.MetricsPoint, 0, len(rows))
	for _, row := range rows {
		out = append(out, vmdomain.MetricsPoint{
			VMID:                 row.VMID,
			CPUUsagePercent:      row.CPUUsagePercent,
			MemoryUsedMB:         row.MemoryUsedMB,
			MemoryLimitMB:        row.MemoryLimitMB,
			MemoryPercent:        row.MemoryPercent,
			DiskUsedMB:           row.DiskUsedMB,
			DiskLimitMB:          row.DiskLimitMB,
			DiskPercent:          row.DiskPercent,
			NetworkBytesSent:     row.NetworkBytesSent,
			NetworkBytesReceived: row.NetworkBytesReceived,
			CollectedAt:          row.CollectedAt.UTC(),
		})
	}

	return out, nil
}

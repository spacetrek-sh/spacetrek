package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"encoding/json"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/snapshot"
)

type snapshotRepository struct {
	db *DB
}

type snapshotRow struct {
	ID               string          `db:"id"`
	VMID             string          `db:"vm_id"`
	ParentSnapshotID *string         `db:"parent_snapshot_id"`
	Type             string          `db:"type"`
	SnapshotPath     string          `db:"snapshot_path"`
	SizeBytes        int64           `db:"size_bytes"`
	Metadata         json.RawMessage `db:"metadata"`
	CreatedAt        time.Time       `db:"created_at"`
}

// NewSnapshotRepository creates a snapshot repository backed by PostgreSQL.
func NewSnapshotRepository(db *DB) snapshot.Repository {
	return &snapshotRepository{db: db}
}

func (r *snapshotRepository) Create(ctx context.Context, snap *snapshot.Snapshot) error {
	logger := pkglog.FromContext(ctx)
	query := `
		INSERT INTO vm_snapshots (id, vm_id, parent_snapshot_id, type, snapshot_path, size_bytes, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`

	if _, err := r.db.ExecContext(ctx, query,
		snap.ID, snap.VMID, snap.ParentSnapshotID, string(snap.Type),
		snap.SnapshotPath, snap.SizeBytes, snap.Metadata, snap.CreatedAt,
	); err != nil {
		logger.ErrorContext(ctx, "postgres: create snapshot failed", "snapshot_id", snap.ID, "vm_id", snap.VMID, "error", err)
		return exception.Internal(fmt.Errorf("create snapshot: %w", err))
	}

	return nil
}

func (r *snapshotRepository) GetByID(ctx context.Context, id string) (*snapshot.Snapshot, error) {
	logger := pkglog.FromContext(ctx)
	query := `
		SELECT id, vm_id, parent_snapshot_id, type, snapshot_path, size_bytes, metadata, created_at
		FROM vm_snapshots
		WHERE id = $1
	`

	var row snapshotRow
	if err := r.db.GetContext(ctx, &row, query, id); err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("snapshot", id)
		}
		logger.ErrorContext(ctx, "postgres: get snapshot by id failed", "snapshot_id", id, "error", err)
		return nil, exception.Internal(fmt.Errorf("get snapshot by id: %w", err))
	}

	return mapSnapshotRow(row), nil
}

func (r *snapshotRepository) GetByVMID(ctx context.Context, vmID string) ([]*snapshot.Snapshot, error) {
	logger := pkglog.FromContext(ctx)
	query := `
		SELECT id, vm_id, parent_snapshot_id, type, snapshot_path, size_bytes, metadata, created_at
		FROM vm_snapshots
		WHERE vm_id = $1
		ORDER BY created_at DESC
	`

	var rows []snapshotRow
	if err := r.db.SelectContext(ctx, &rows, query, vmID); err != nil {
		logger.ErrorContext(ctx, "postgres: get snapshots by vm_id failed", "vm_id", vmID, "error", err)
		return nil, exception.Internal(fmt.Errorf("get snapshots by vm_id: %w", err))
	}

	result := make([]*snapshot.Snapshot, len(rows))
	for i, row := range rows {
		result[i] = mapSnapshotRow(row)
	}
	return result, nil
}

func (r *snapshotRepository) GetLatestFull(ctx context.Context, vmID string) (*snapshot.Snapshot, error) {
	logger := pkglog.FromContext(ctx)
	query := `
		SELECT id, vm_id, parent_snapshot_id, type, snapshot_path, size_bytes, metadata, created_at
		FROM vm_snapshots
		WHERE vm_id = $1 AND type = 'full'
		ORDER BY created_at DESC
		LIMIT 1
	`

	var row snapshotRow
	if err := r.db.GetContext(ctx, &row, query, vmID); err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("snapshot", vmID)
		}
		logger.ErrorContext(ctx, "postgres: get latest full snapshot failed", "vm_id", vmID, "error", err)
		return nil, exception.Internal(fmt.Errorf("get latest full snapshot: %w", err))
	}

	return mapSnapshotRow(row), nil
}

func (r *snapshotRepository) GetLatestByVMID(ctx context.Context, vmID string) (*snapshot.Snapshot, error) {
	logger := pkglog.FromContext(ctx)
	query := `
		SELECT id, vm_id, parent_snapshot_id, type, snapshot_path, size_bytes, metadata, created_at
		FROM vm_snapshots
		WHERE vm_id = $1
		ORDER BY created_at DESC
		LIMIT 1
	`

	var row snapshotRow
	if err := r.db.GetContext(ctx, &row, query, vmID); err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("snapshot", vmID)
		}
		logger.ErrorContext(ctx, "postgres: get latest snapshot failed", "vm_id", vmID, "error", err)
		return nil, exception.Internal(fmt.Errorf("get latest snapshot: %w", err))
	}

	return mapSnapshotRow(row), nil
}

func (r *snapshotRepository) Delete(ctx context.Context, id string) error {
	logger := pkglog.FromContext(ctx)
	query := `DELETE FROM vm_snapshots WHERE id = $1`

	result, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: delete snapshot failed", "snapshot_id", id, "error", err)
		return exception.Internal(fmt.Errorf("delete snapshot: %w", err))
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return exception.Internal(fmt.Errorf("delete snapshot rows affected: %w", err))
	}
	if rowsAffected == 0 {
		return exception.NotFound("snapshot", id)
	}

	return nil
}

func mapSnapshotRow(row snapshotRow) *snapshot.Snapshot {
	return &snapshot.Snapshot{
		ID:               row.ID,
		VMID:             row.VMID,
		ParentSnapshotID: row.ParentSnapshotID,
		Type:             snapshot.Type(row.Type),
		SnapshotPath:     row.SnapshotPath,
		SizeBytes:        row.SizeBytes,
		Metadata:         row.Metadata,
		CreatedAt:        row.CreatedAt,
	}
}

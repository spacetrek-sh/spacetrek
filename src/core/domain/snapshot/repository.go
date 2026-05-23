// Package snapshot defines the snapshot repository interface.
package snapshot

import (
	"context"
	"time"
)

// Repository defines the persistence contract for Snapshot entities.
type Repository interface {
	Create(ctx context.Context, snap *Snapshot) error
	GetByID(ctx context.Context, id string) (*Snapshot, error)
	GetByVMID(ctx context.Context, vmID string) ([]*Snapshot, error)
	GetLatestFull(ctx context.Context, vmID string) (*Snapshot, error)
	GetLatestByVMID(ctx context.Context, vmID string) (*Snapshot, error)
	Delete(ctx context.Context, id string) error

	// ListOrphaned returns snapshots belonging to terminated VMs older than the threshold.
	ListOrphaned(ctx context.Context, olderThan time.Duration) ([]*Snapshot, error)
}

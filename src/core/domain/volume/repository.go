// Package volume defines the volume repository interface.
package volume

import "context"

// Repository defines the persistence contract for Volume entities.
type Repository interface {
	Create(ctx context.Context, vol *Volume) error
	GetByID(ctx context.Context, id string) (*Volume, error)
	GetByVMID(ctx context.Context, vmID string) ([]*Volume, error)
	Delete(ctx context.Context, id string) error
}

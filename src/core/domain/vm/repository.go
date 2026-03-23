// Package vm defines the VM repository interface.
package vm

import "context"

// Repository defines the persistence contract for VM entities.
type Repository interface {
	// Basic CRUD
	Create(ctx context.Context, vm *VM) error
	GetByID(ctx context.Context, id string) (*VM, error)
	Update(ctx context.Context, vm *VM) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]*VM, error)

	// Pool management queries (matching DB indexes)
	GetAvailablePool(ctx context.Context, provider Provider, limit int) ([]*VM, error)
	GetByEnvironmentID(ctx context.Context, envID string) ([]*VM, error)
	GetByChatID(ctx context.Context, chatID string) (*VM, error)
	GetActiveVMs(ctx context.Context) ([]*VM, error)
}

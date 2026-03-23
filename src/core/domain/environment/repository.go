// Package environment defines the environment repository interface.
package environment

import "context"

// Repository defines the persistence contract for Environment entities.
type Repository interface {
	Create(ctx context.Context, env *Environment) error
	GetByID(ctx context.Context, id string) (*Environment, error)
	List(ctx context.Context) ([]*Environment, error)
	Update(ctx context.Context, env *Environment) error
	Delete(ctx context.Context, id string) error
}

// Package user defines the user repository interface.
package user

import "context"

// Repository defines the interface for user data persistence.
type Repository interface {
	// Create creates a new user with the given parameters.
	Create(ctx context.Context, p CreateParams) (*User, error)

	// GetByID retrieves a user by ID. Returns NotFound if not found.
	GetByID(ctx context.Context, id string) (*User, error)

	// GetByEmail retrieves a user by email. Returns NotFound if not found.
	GetByEmail(ctx context.Context, email string) (*User, error)

	// GetByUsername retrieves a user by username. Returns NotFound if not found.
	GetByUsername(ctx context.Context, username string) (*User, error)

	// Update updates a user with the given parameters.
	// Only non-nil fields in UpdateParams will be updated.
	Update(ctx context.Context, id string, p UpdateParams) (*User, error)

	// Delete soft-deletes a user by setting deleted_at.
	Delete(ctx context.Context, id string) error

	// UpdateLastLogin updates the last_login_at timestamp for a user.
	UpdateLastLogin(ctx context.Context, id string) error
}

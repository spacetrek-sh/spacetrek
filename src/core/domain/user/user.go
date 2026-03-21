// Package user defines the user domain entity and related types.
package user

import "time"

// UserRole represents the user's role in the system.
type UserRole string

const (
	// RoleAdmin represents an administrator user with full permissions.
	RoleAdmin UserRole = "admin"
	// RoleUser represents a regular user.
	RoleUser UserRole = "user"
)

// User represents a user account in the system.
type User struct {
	ID           string     `db:"id"`
	Username     string     `db:"username"`
	Email        string     `db:"email"`
	PasswordHash string     `db:"password_hash"` // Never exposed in responses
	Role         UserRole   `db:"role"`
	IsVerified   bool       `db:"is_verified"`
	VerifiedAt   *time.Time `db:"verified_at"`
	LastLoginAt  *time.Time `db:"last_login_at"`
	CreatedAt    time.Time  `db:"created_at"`
	UpdatedAt    time.Time  `db:"updated_at"`
	DeletedAt    *time.Time `db:"deleted_at"` // Soft delete
}

// CreateParams contains the parameters for creating a new user.
type CreateParams struct {
	Username     string
	Email        string
	PasswordHash string
	Role         UserRole // Defaults to RoleUser if empty
}

// UpdateParams contains the parameters for updating a user.
// Only non-nil fields will be updated.
type UpdateParams struct {
	Username     *string
	Email        *string
	PasswordHash *string
	IsVerified   *bool
}

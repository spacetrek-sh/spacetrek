// Package postgres provides the PostgreSQL implementation of the user repository.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/kumori-sh/spacetrk/pkg/exception"
	"github.com/kumori-sh/spacetrk/src/core/domain/user"
)

type userRepository struct {
	db *DB
}

// NewUserRepository creates a new user repository backed by PostgreSQL.
func NewUserRepository(db *DB) user.Repository {
	return &userRepository{db: db}
}

// Create creates a new user in the database.
func (r *userRepository) Create(ctx context.Context, p user.CreateParams) (*user.User, error) {
	now := time.Now()
	role := p.Role
	if role == "" {
		role = user.RoleUser
	}

	query := `
		INSERT INTO users (username, email, password_hash, role, is_verified, verified_at, created_at, updated_at)
		VALUES (:username, :email, :password_hash, :role, :is_verified, :verified_at, :created_at, :updated_at)
		RETURNING id, username, email, password_hash, role, is_verified, verified_at, last_login_at, created_at, updated_at, deleted_at
	`

	args := map[string]interface{}{
		"username":      p.Username,
		"email":         p.Email,
		"password_hash": p.PasswordHash,
		"role":          role,
		"is_verified":   true, // Verified by default - email verification deferred
		"verified_at":   now,
		"created_at":    now,
		"updated_at":    now,
	}

	rows, err := r.db.NamedQueryContext(ctx, query, args)
	if err != nil {
		return nil, exception.Internal(fmt.Errorf("create user: %w", err))
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, exception.Internal(fmt.Errorf("create user: no row returned"))
	}

	var u user.User
	if err := rows.StructScan(&u); err != nil {
		return nil, exception.Internal(fmt.Errorf("create user: scan: %w", err))
	}

	return &u, nil
}

// GetByID retrieves a user by ID.
func (r *userRepository) GetByID(ctx context.Context, id string) (*user.User, error) {
	query := `
		SELECT id, username, email, password_hash, role, is_verified, verified_at, last_login_at, created_at, updated_at, deleted_at
		FROM users
		WHERE id = $1 AND deleted_at IS NULL
	`

	var u user.User
	err := r.db.GetContext(ctx, &u, query, id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("user", id)
		}
		return nil, exception.Internal(fmt.Errorf("get user by id: %w", err))
	}

	return &u, nil
}

// GetByEmail retrieves a user by email.
func (r *userRepository) GetByEmail(ctx context.Context, email string) (*user.User, error) {
	query := `
		SELECT id, username, email, password_hash, role, is_verified, verified_at, last_login_at, created_at, updated_at, deleted_at
		FROM users
		WHERE email = $1 AND deleted_at IS NULL
	`

	var u user.User
	err := r.db.GetContext(ctx, &u, query, email)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("user", email)
		}
		return nil, exception.Internal(fmt.Errorf("get user by email: %w", err))
	}

	return &u, nil
}

// GetByUsername retrieves a user by username.
func (r *userRepository) GetByUsername(ctx context.Context, username string) (*user.User, error) {
	query := `
		SELECT id, username, email, password_hash, role, is_verified, verified_at, last_login_at, created_at, updated_at, deleted_at
		FROM users
		WHERE username = $1 AND deleted_at IS NULL
	`

	var u user.User
	err := r.db.GetContext(ctx, &u, query, username)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("user", username)
		}
		return nil, exception.Internal(fmt.Errorf("get user by username: %w", err))
	}

	return &u, nil
}

// Update updates a user with the given parameters.
func (r *userRepository) Update(ctx context.Context, id string, p user.UpdateParams) (*user.User, error) {
	// Build dynamic update query based on non-nil fields
	sets := []string{"updated_at = :updated_at"}
	args := map[string]interface{}{
		"id":         id,
		"updated_at": time.Now(),
	}

	if p.Username != nil {
		sets = append(sets, "username = :username")
		args["username"] = *p.Username
	}
	if p.Email != nil {
		sets = append(sets, "email = :email")
		args["email"] = *p.Email
	}
	if p.PasswordHash != nil {
		sets = append(sets, "password_hash = :password_hash")
		args["password_hash"] = *p.PasswordHash
	}
	if p.IsVerified != nil {
		sets = append(sets, "is_verified = :is_verified")
		args["is_verified"] = *p.IsVerified
	}

	query := fmt.Sprintf(`
		UPDATE users
		SET %s
		WHERE id = :id AND deleted_at IS NULL
		RETURNING id, username, email, password_hash, role, is_verified, verified_at, last_login_at, created_at, updated_at, deleted_at
	`, joinSetClause(sets))

	rows, err := r.db.NamedQueryContext(ctx, query, args)
	if err != nil {
		return nil, exception.Internal(fmt.Errorf("update user: %w", err))
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, exception.NotFound("user", id)
	}

	var u user.User
	if err := rows.StructScan(&u); err != nil {
		return nil, exception.Internal(fmt.Errorf("update user: scan: %w", err))
	}

	return &u, nil
}

// Delete soft-deletes a user by setting deleted_at.
func (r *userRepository) Delete(ctx context.Context, id string) error {
	query := `
		UPDATE users
		SET deleted_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL
	`

	result, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return exception.Internal(fmt.Errorf("delete user: %w", err))
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return exception.Internal(fmt.Errorf("delete user: rows affected: %w", err))
	}

	if rowsAffected == 0 {
		return exception.NotFound("user", id)
	}

	return nil
}

// UpdateLastLogin updates the last_login_at timestamp for a user.
func (r *userRepository) UpdateLastLogin(ctx context.Context, id string) error {
	query := `
		UPDATE users
		SET last_login_at = NOW(), updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL
	`

	result, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return exception.Internal(fmt.Errorf("update last login: %w", err))
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return exception.Internal(fmt.Errorf("update last login: rows affected: %w", err))
	}

	if rowsAffected == 0 {
		return exception.NotFound("user", id)
	}

	return nil
}

// joinSetClause joins set clauses with commas.
func joinSetClause(clauses []string) string {
	if len(clauses) == 0 {
		return ""
	}
	result := clauses[0]
	for i := 1; i < len(clauses); i++ {
		result += ", " + clauses[i]
	}
	return result
}

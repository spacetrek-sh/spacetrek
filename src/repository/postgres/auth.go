// Package postgres provides the PostgreSQL implementation of the auth repository.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/kumori-sh/spacetrk/pkg/exception"
	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	"github.com/kumori-sh/spacetrk/src/core/domain/auth"
)

type authRepository struct {
	db *DB
}

// NewAuthRepository creates a new auth repository backed by PostgreSQL.
func NewAuthRepository(db *DB) auth.Repository {
	return &authRepository{db: db}
}

// StoreRefreshToken stores a new refresh token for the given user.
func (r *authRepository) StoreRefreshToken(ctx context.Context, userID, tokenHash string, expiresAt time.Time) (*auth.RefreshToken, error) {
	logger := pkglog.FromContext(ctx)

	id := uuid.New().String()
	now := time.Now()

	query := `
		INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, created_at)
		VALUES (:id, :user_id, :token_hash, :expires_at, :created_at)
		RETURNING id, user_id, token_hash, expires_at, revoked_at, created_at
	`

	args := map[string]interface{}{
		"id":         id,
		"user_id":    userID,
		"token_hash": tokenHash,
		"expires_at": expiresAt,
		"created_at": now,
	}

	rows, err := r.db.NamedQueryContext(ctx, query, args)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: store refresh token failed", "user_id", userID, "error", err)
		return nil, exception.Internal(fmt.Errorf("store refresh token: %w", err))
	}
	defer rows.Close()

	if !rows.Next() {
		logger.ErrorContext(ctx, "postgres: store refresh token failed", "user_id", userID, "error", "no row returned")
		return nil, exception.Internal(fmt.Errorf("store refresh token: no row returned"))
	}

	var rt auth.RefreshToken
	if err := rows.StructScan(&rt); err != nil {
		logger.ErrorContext(ctx, "postgres: store refresh token scan failed", "user_id", userID, "error", err)
		return nil, exception.Internal(fmt.Errorf("store refresh token: scan: %w", err))
	}

	return &rt, nil
}

// GetRefreshToken retrieves a refresh token by its hash.
func (r *authRepository) GetRefreshToken(ctx context.Context, tokenHash string) (*auth.RefreshToken, error) {
	logger := pkglog.FromContext(ctx)

	query := `
		SELECT id, user_id, token_hash, expires_at, revoked_at, created_at
		FROM refresh_tokens
		WHERE token_hash = $1
	`

	var rt auth.RefreshToken
	err := r.db.GetContext(ctx, &rt, query, tokenHash)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("refresh token", tokenHash[:8]+"...")
		}
		logger.ErrorContext(ctx, "postgres: get refresh token failed", "error", err)
		return nil, exception.Internal(fmt.Errorf("get refresh token: %w", err))
	}

	return &rt, nil
}

// RevokeRefreshToken revokes a refresh token by setting revoked_at.
func (r *authRepository) RevokeRefreshToken(ctx context.Context, tokenHash string) error {
	logger := pkglog.FromContext(ctx)

	query := `
		UPDATE refresh_tokens
		SET revoked_at = NOW()
		WHERE token_hash = $1 AND revoked_at IS NULL
	`

	result, err := r.db.ExecContext(ctx, query, tokenHash)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: revoke refresh token failed", "error", err)
		return exception.Internal(fmt.Errorf("revoke refresh token: %w", err))
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		logger.ErrorContext(ctx, "postgres: revoke refresh token rows affected failed", "error", err)
		return exception.Internal(fmt.Errorf("revoke refresh token: rows affected: %w", err))
	}

	if rowsAffected == 0 {
		return exception.NotFound("refresh token", tokenHash[:8]+"...")
	}

	return nil
}

// RevokeAllUserTokens revokes all refresh tokens for a user.
func (r *authRepository) RevokeAllUserTokens(ctx context.Context, userID string) error {
	logger := pkglog.FromContext(ctx)

	query := `
		UPDATE refresh_tokens
		SET revoked_at = NOW()
		WHERE user_id = $1 AND revoked_at IS NULL
	`

	_, err := r.db.ExecContext(ctx, query, userID)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: revoke all user tokens failed", "user_id", userID, "error", err)
		return exception.Internal(fmt.Errorf("revoke all user tokens: %w", err))
	}

	return nil
}

// CleanupExpiredTokens deletes expired and revoked tokens from the database.
func (r *authRepository) CleanupExpiredTokens(ctx context.Context) error {
	logger := pkglog.FromContext(ctx)

	query := `
		DELETE FROM refresh_tokens
		WHERE revoked_at IS NOT NULL OR expires_at < NOW()
	`

	_, err := r.db.ExecContext(ctx, query)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: cleanup expired tokens failed", "error", err)
		return exception.Internal(fmt.Errorf("cleanup expired tokens: %w", err))
	}

	return nil
}

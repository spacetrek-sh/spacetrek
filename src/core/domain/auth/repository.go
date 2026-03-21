// Package auth defines the authentication repository interface.
package auth

import (
	"context"
	"time"
)

// Repository defines the interface for authentication data persistence.
type Repository interface {
	// StoreRefreshToken stores a new refresh token for the given user.
	StoreRefreshToken(ctx context.Context, userID, tokenHash string, expiresAt time.Time) (*RefreshToken, error)

	// GetRefreshToken retrieves a refresh token by its hash.
	// Returns NotFound if not found.
	GetRefreshToken(ctx context.Context, tokenHash string) (*RefreshToken, error)

	// RevokeRefreshToken revokes a refresh token by setting revoked_at.
	RevokeRefreshToken(ctx context.Context, tokenHash string) error

	// RevokeAllUserTokens revokes all refresh tokens for a user.
	RevokeAllUserTokens(ctx context.Context, userID string) error

	// CleanupExpiredTokens deletes expired and revoked tokens from the database.
	// This should be run periodically as a background job.
	CleanupExpiredTokens(ctx context.Context) error
}

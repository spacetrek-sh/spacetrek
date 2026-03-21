// Package auth defines the authentication domain entities.
package auth

import "time"

// RefreshToken represents a refresh token stored in the database.
type RefreshToken struct {
	ID        string     `db:"id"`
	UserID    string     `db:"user_id"`
	TokenHash string     `db:"token_hash"`
	ExpiresAt time.Time  `db:"expires_at"`
	RevokedAt *time.Time `db:"revoked_at"`
	CreatedAt time.Time  `db:"created_at"`
}

// IsActive returns true if the token has not been revoked and has not expired.
func (rt *RefreshToken) IsActive() bool {
	if rt.RevokedAt != nil {
		return false
	}
	return time.Now().Before(rt.ExpiresAt)
}

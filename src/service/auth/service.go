// Package auth provides the authentication service for token management.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"time"

	"github.com/kumori-sh/spacetrk/pkg/auth/jwt"
	"github.com/kumori-sh/spacetrk/pkg/exception"
	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	authdomain "github.com/kumori-sh/spacetrk/src/core/domain/auth"
	userdomain "github.com/kumori-sh/spacetrk/src/core/domain/user"
)

const (
	// RefreshTokenLength is the length of the refresh token in bytes.
	RefreshTokenLength = 32
)

// Service handles authentication business logic.
type Service struct {
	jwtManager *jwt.Manager
	repo       authdomain.Repository
	userRepo   userdomain.Repository
}

// NewService creates a new auth service.
func NewService(jwtMgr *jwt.Manager, authRepo authdomain.Repository, userRepo userdomain.Repository) *Service {
	return &Service{
		jwtManager: jwtMgr,
		repo:       authRepo,
		userRepo:   userRepo,
	}
}

// GenerateTokenPair creates an access token and refresh token for the given user.
func (s *Service) GenerateTokenPair(ctx context.Context, userID string) (accessToken, refreshToken string, expiresAt time.Time, err error) {
	logger := pkglog.FromContext(ctx)

	// Get user to retrieve role
	u, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		logger.WarnContext(ctx, "failed to get user for token generation", "user_id", userID, "error", err)
		return "", "", time.Time{}, err
	}

	// Generate access token
	accessToken, expiresAt, err = s.jwtManager.GenerateAccessToken(u.ID, string(u.Role))
	if err != nil {
		logger.ErrorContext(ctx, "failed to generate access token", "user_id", userID, "error", err)
		return "", "", time.Time{}, exception.Internal(err)
	}

	// Generate refresh token
	refreshToken, err = generateRefreshToken()
	if err != nil {
		logger.ErrorContext(ctx, "failed to generate refresh token", "user_id", userID, "error", err)
		return "", "", time.Time{}, exception.Internal(err)
	}

	// Hash refresh token for storage
	tokenHash := hashToken(refreshToken)

	// Calculate refresh token expiry (30 days from now)
	refreshExpiry := time.Now().Add(30 * 24 * time.Hour)

	// Store refresh token
	_, err = s.repo.StoreRefreshToken(ctx, userID, tokenHash, refreshExpiry)
	if err != nil {
		logger.ErrorContext(ctx, "failed to store refresh token", "user_id", userID, "error", err)
		return "", "", time.Time{}, err
	}

	logger.DebugContext(ctx, "token pair generated", "user_id", userID, "expires_at", expiresAt)
	return accessToken, refreshToken, expiresAt, nil
}

// ValidateRefreshToken validates a refresh token and returns the associated user.
func (s *Service) ValidateRefreshToken(ctx context.Context, token string) (*userdomain.User, error) {
	logger := pkglog.FromContext(ctx)

	// Hash the token to look it up
	tokenHash := hashToken(token)

	// Get refresh token from database
	rt, err := s.repo.GetRefreshToken(ctx, tokenHash)
	if err != nil {
		logger.WarnContext(ctx, "refresh token not found", "error", err)
		return nil, exception.Unauthorized("invalid refresh token")
	}

	// Check if token is active
	if !rt.IsActive() {
		logger.WarnContext(ctx, "refresh token is inactive", "token_id", rt.ID, "user_id", rt.UserID)
		return nil, exception.Unauthorized("refresh token expired or revoked")
	}

	// Get user
	u, err := s.userRepo.GetByID(ctx, rt.UserID)
	if err != nil {
		logger.WarnContext(ctx, "user not found for refresh token", "token_id", rt.ID, "user_id", rt.UserID)
		return nil, exception.Unauthorized("user not found")
	}

	logger.DebugContext(ctx, "refresh token validated", "user_id", u.ID)
	return u, nil
}

// RevokeRefreshToken revokes a refresh token.
func (s *Service) RevokeRefreshToken(ctx context.Context, token string) error {
	logger := pkglog.FromContext(ctx)
	tokenHash := hashToken(token)

	err := s.repo.RevokeRefreshToken(ctx, tokenHash)
	if err != nil {
		logger.WarnContext(ctx, "failed to revoke refresh token", "error", err)
		return err
	}

	logger.DebugContext(ctx, "refresh token revoked")
	return nil
}

// RevokeAllUserTokens revokes all refresh tokens for a user.
func (s *Service) RevokeAllUserTokens(ctx context.Context, userID string) error {
	logger := pkglog.FromContext(ctx)

	err := s.repo.RevokeAllUserTokens(ctx, userID)
	if err != nil {
		logger.ErrorContext(ctx, "failed to revoke all user tokens", "user_id", userID, "error", err)
		return err
	}

	logger.DebugContext(ctx, "all user tokens revoked", "user_id", userID)
	return nil
}

// generateRefreshToken generates a cryptographically secure random refresh token.
func generateRefreshToken() (string, error) {
	b := make([]byte, RefreshTokenLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// hashToken creates a SHA-256 hash of the token for storage.
func hashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

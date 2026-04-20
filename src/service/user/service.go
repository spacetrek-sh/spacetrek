// Package user provides the user service for user management operations.
package user

import (
	"context"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/user"
)

// Service handles user business logic.
type Service struct {
	repo user.Repository
}

// NewService creates a new user service.
func NewService(repo user.Repository) *Service {
	return &Service{repo: repo}
}

// Register creates a new user account.
// The user is verified by default - email verification is deferred.
func (s *Service) Register(ctx context.Context, username, email, password string) (*user.User, error) {
	logger := pkglog.FromContext(ctx)

	// Hash the password
	passwordHash, err := HashPassword(password)
	if err != nil {
		logger.ErrorContext(ctx, "failed to hash password", "error", err)
		return nil, exception.Internal(err)
	}

	// Create the user
	u, err := s.repo.Create(ctx, user.CreateParams{
		Username:     username,
		Email:        email,
		PasswordHash: passwordHash,
		Role:         user.RoleUser, // Default to regular user
	})
	if err != nil {
		logger.WarnContext(ctx, "failed to create user", "email", email, "username", username, "error", err)
		return nil, err
	}

	logger.DebugContext(ctx, "user created", "user_id", u.ID, "username", u.Username, "email", u.Email)
	return u, nil
}

// Login validates credentials and returns the user if valid.
// Token generation is handled by the auth layer.
func (s *Service) Login(ctx context.Context, email, password string) (*user.User, error) {
	logger := pkglog.FromContext(ctx)

	// Get user by email
	u, err := s.repo.GetByEmail(ctx, email)
	if err != nil {
		logger.WarnContext(ctx, "login attempt with unknown email", "email", email)
		return nil, exception.Unauthorized("invalid email or password")
	}

	// Validate password
	if !ValidatePassword(password, u.PasswordHash) {
		logger.WarnContext(ctx, "login attempt with invalid password", "email", email, "user_id", u.ID)
		return nil, exception.Unauthorized("invalid email or password")
	}

	// Update last login
	if err := s.repo.UpdateLastLogin(ctx, u.ID); err != nil {
		// Log warning but don't fail login
		logger.WarnContext(ctx, "failed to update last login", "user_id", u.ID, "error", err)
	}

	logger.DebugContext(ctx, "user logged in successfully", "user_id", u.ID, "email", u.Email)
	return u, nil
}

// GetByID retrieves a user by ID.
func (s *Service) GetByID(ctx context.Context, id string) (*user.User, error) {
	return s.repo.GetByID(ctx, id)
}

// UpdateProfile updates user profile fields.
func (s *Service) UpdateProfile(ctx context.Context, id string, p user.UpdateParams) (*user.User, error) {
	return s.repo.Update(ctx, id, p)
}

// ChangePassword updates a user's password.
func (s *Service) ChangePassword(ctx context.Context, id, oldPassword, newPassword string) error {
	logger := pkglog.FromContext(ctx)

	// Get user to verify old password
	u, err := s.repo.GetByID(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "failed to get user for password change", "user_id", id, "error", err)
		return err
	}

	// Validate old password
	if !ValidatePassword(oldPassword, u.PasswordHash) {
		logger.WarnContext(ctx, "password change attempt with invalid old password", "user_id", id)
		return exception.Unauthorized("invalid current password")
	}

	// Hash new password
	newHash, err := HashPassword(newPassword)
	if err != nil {
		logger.ErrorContext(ctx, "failed to hash new password", "user_id", id, "error", err)
		return exception.Internal(err)
	}

	// Update password
	_, err = s.repo.Update(ctx, id, user.UpdateParams{
		PasswordHash: &newHash,
	})
	if err != nil {
		logger.WarnContext(ctx, "failed to update password", "user_id", id, "error", err)
		return err
	}

	logger.InfoContext(ctx, "password changed successfully", "user_id", id)
	return nil
}

// Package authhttp provides HTTP request/response DTOs for authentication endpoints.
package authhttp

import "time"

// registerRequest is the JSON body for POST /api/v1/auth/register.
type registerRequest struct {
	Username string `json:"username" validate:"required,min=3,max=50"`
	Email    string `json:"email"    validate:"required,email"`
	Password string `json:"password" validate:"required,min=8"`
}

// loginRequest is the JSON body for POST /api/v1/auth/login.
type loginRequest struct {
	Email    string `json:"email"    validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

// refreshTokenRequest is the JSON body for POST /api/v1/auth/refresh.
type refreshTokenRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

// tokenResponse is the JSON response for successful login/refresh.
type tokenResponse struct {
	AccessToken  string       `json:"access_token"`
	RefreshToken string       `json:"refresh_token"`
	ExpiresAt    time.Time    `json:"expires_at"`
	User         *userResponse `json:"user"`
}

// userResponse is the JSON representation of a user (without sensitive data).
type userResponse struct {
	ID         string    `json:"id"`
	Username   string    `json:"username"`
	Email      string    `json:"email"`
	Role       string    `json:"role"`
	IsVerified bool      `json:"is_verified"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// changePasswordRequest is the JSON body for PUT /api/v1/auth/password.
type changePasswordRequest struct {
	OldPassword string `json:"old_password" validate:"required"`
	NewPassword string `json:"new_password" validate:"required,min=8"`
}

// updateProfileRequest is the JSON body for PUT /api/v1/auth/profile.
type updateProfileRequest struct {
	Username *string `json:"username" validate:"omitempty,min=3,max=50"`
	Email    *string `json:"email"    validate:"omitempty,email"`
}

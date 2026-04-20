// Package authhttp provides HTTP handlers for authentication endpoints.
package authhttp

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/spacetrek-sh/spacetrek/pkg/auth/jwt"
	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	httputil "github.com/spacetrek-sh/spacetrek/pkg/http"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/middleware"
	authservice "github.com/spacetrek-sh/spacetrek/src/service/auth"
	userservice "github.com/spacetrek-sh/spacetrek/src/service/user"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/user"
)

// Handler groups all authentication-related HTTP handlers.
type Handler struct {
	userService *userservice.Service
	authService *authservice.Service
	jwtManager  *jwt.Manager
}

// NewHandler creates a new auth handler.
func NewHandler(userSvc *userservice.Service, authSvc *authservice.Service, jwtMgr *jwt.Manager) *Handler {
	return &Handler{
		userService: userSvc,
		authService: authSvc,
		jwtManager:  jwtMgr,
	}
}

// RegisterRoutes registers all auth routes under the given router.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/auth", func(r chi.Router) {
		r.Post("/register", h.Register)
		r.Post("/login", h.Login)
		r.Post("/refresh", h.Refresh)

		// Authenticated routes
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticate(h.jwtManager))
			r.Post("/logout", h.Logout)
			r.Get("/me", h.Me)
			r.Put("/profile", h.UpdateProfile)
			r.Put("/password", h.ChangePassword)
		})
	})
}

// Register handles POST /api/v1/auth/register
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	var req registerRequest
	if err := httputil.BindJSON(r, &req); err != nil {
		logger.WarnContext(ctx, "registration failed", "error", err)
		httputil.WriteError(w, err)
		return
	}

	u, err := h.userService.Register(ctx, req.Username, req.Email, req.Password)
	if err != nil {
		logger.WarnContext(ctx, "registration failed", "email", req.Email, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "user registered", "user_id", u.ID, "username", u.Username, "email", u.Email)
	httputil.Created(w, "user registered", toUserResponse(u))
}

// Login handles POST /api/v1/auth/login
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	var req loginRequest
	if err := httputil.BindJSON(r, &req); err != nil {
		logger.WarnContext(ctx, "login failed", "error", err)
		httputil.WriteError(w, err)
		return
	}

	u, err := h.userService.Login(ctx, req.Email, req.Password)
	if err != nil {
		logger.WarnContext(ctx, "login failed", "email", req.Email, "error", err)
		httputil.WriteError(w, err)
		return
	}

	accessToken, refreshToken, expiresAt, err := h.authService.GenerateTokenPair(ctx, u.ID)
	if err != nil {
		logger.ErrorContext(ctx, "token generation failed", "user_id", u.ID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(r.Context(), "user logged in", "user_id", u.ID, "email", u.Email)
	httputil.WriteJSON(w, http.StatusOK, "login successful", &tokenResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		User:         toUserResponse(u),
	})
}

// Refresh handles POST /api/v1/auth/refresh
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	var req refreshTokenRequest
	if err := httputil.BindJSON(r, &req); err != nil {
		logger.WarnContext(ctx, "token refresh failed", "error", err)
		httputil.WriteError(w, err)
		return
	}

	// Validate refresh token and get user
	u, err := h.authService.ValidateRefreshToken(ctx, req.RefreshToken)
	if err != nil {
		logger.WarnContext(ctx, "token refresh failed", "error", err)
		httputil.WriteError(w, err)
		return
	}

	// Revoke old refresh token
	if err := h.authService.RevokeRefreshToken(ctx, req.RefreshToken); err != nil {
		logger.ErrorContext(ctx, "failed to revoke old refresh token", "user_id", u.ID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	// Generate new token pair
	accessToken, refreshToken, expiresAt, err := h.authService.GenerateTokenPair(ctx, u.ID)
	if err != nil {
		logger.ErrorContext(ctx, "token generation failed during refresh", "user_id", u.ID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "token refreshed", "user_id", u.ID)
	httputil.WriteJSON(w, http.StatusOK, "token refreshed", &tokenResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		User:         toUserResponse(u),
	})
}

// Logout handles POST /api/v1/auth/logout
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	userID := middleware.GetUserID(ctx)
	if userID == "" {
		httputil.WriteError(w, exception.Unauthorized("user not authenticated"))
		return
	}

	// Revoke all refresh tokens for this user
	if err := h.authService.RevokeAllUserTokens(ctx, userID); err != nil {
		logger.ErrorContext(ctx, "logout failed", "user_id", userID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "user logged out", "user_id", userID)
	httputil.WriteJSON(w, http.StatusOK, "logged out successfully", nil)
}

// Me handles GET /api/v1/auth/me
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	userID := middleware.GetUserID(ctx)
	if userID == "" {
		logger.WarnContext(ctx, "me endpoint accessed without authentication")
		httputil.WriteError(w, exception.Unauthorized("user not authenticated"))
		return
	}

	u, err := h.userService.GetByID(ctx, userID)
	if err != nil {
		logger.WarnContext(ctx, "failed to retrieve user info", "user_id", userID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, "user retrieved", toUserResponse(u))
}

// UpdateProfile handles PUT /api/v1/auth/profile
func (h *Handler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	userID := middleware.GetUserID(ctx)
	if userID == "" {
		httputil.WriteError(w, exception.Unauthorized("user not authenticated"))
		return
	}

	var req updateProfileRequest
	if err := httputil.BindJSON(r, &req); err != nil {
		logger.WarnContext(ctx, "profile update failed", "user_id", userID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	// Build update params
	params := user.UpdateParams{}
	if req.Username != nil {
		params.Username = req.Username
	}
	if req.Email != nil {
		params.Email = req.Email
	}

	u, err := h.userService.UpdateProfile(ctx, userID, params)
	if err != nil {
		logger.WarnContext(ctx, "profile update failed", "user_id", userID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "profile updated", "user_id", userID)
	httputil.WriteJSON(w, http.StatusOK, "profile updated", toUserResponse(u))
}

// ChangePassword handles PUT /api/v1/auth/password
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	userID := middleware.GetUserID(ctx)
	if userID == "" {
		httputil.WriteError(w, exception.Unauthorized("user not authenticated"))
		return
	}

	var req changePasswordRequest
	if err := httputil.BindJSON(r, &req); err != nil {
		logger.WarnContext(ctx, "password change failed", "user_id", userID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	if err := h.userService.ChangePassword(ctx, userID, req.OldPassword, req.NewPassword); err != nil {
		logger.WarnContext(ctx, "password change failed", "user_id", userID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "password changed", "user_id", userID)
	httputil.WriteJSON(w, http.StatusOK, "password changed successfully", nil)
}

// toUserResponse converts a domain User to its JSON representation.
func toUserResponse(u *user.User) *userResponse {
	return &userResponse{
		ID:         u.ID,
		Username:   u.Username,
		Email:      u.Email,
		Role:       string(u.Role),
		IsVerified: u.IsVerified,
		CreatedAt:  u.CreatedAt,
		UpdatedAt:  u.UpdatedAt,
	}
}

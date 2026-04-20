// Package middleware provides HTTP middleware for authentication and authorization.
package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/spacetrek-sh/spacetrek/pkg/auth/jwt"
	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	httputil "github.com/spacetrek-sh/spacetrek/pkg/http"
)

type userContextKey struct{}
type roleContextKey struct{}

// Authenticate validates JWT tokens and adds user information to the request context.
// Tokens are extracted from the Authorization header using the "Bearer" scheme.
func Authenticate(jwtManager *jwt.Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract token from Authorization header
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				httputil.WriteError(w, exception.Unauthorized("missing authorization header"))
				return
			}

			// Check Bearer scheme
			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || parts[0] != "Bearer" {
				httputil.WriteError(w, exception.Unauthorized("invalid authorization header format"))
				return
			}

			tokenString := parts[1]

			// Validate token
			claims, err := jwtManager.ValidateToken(tokenString)
			if err != nil {
				if err == jwt.ErrTokenExpired {
					httputil.WriteError(w, exception.Unauthorized("token expired"))
				} else {
					httputil.WriteError(w, exception.Unauthorized("invalid token"))
				}
				return
			}

			// Add user info to context
			ctx := context.WithValue(r.Context(), userContextKey{}, claims.UserID)
			ctx = context.WithValue(ctx, roleContextKey{}, claims.Role)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole checks if the authenticated user has one of the required roles.
// Must be used after Authenticate middleware.
func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			userRole := GetUserRole(r.Context())
			if userRole == "" {
				httputil.WriteError(w, exception.Unauthorized("user not authenticated"))
				return
			}

			// Check if user has any of the required roles
			allowed := false
			for _, role := range roles {
				if userRole == role {
					allowed = true
					break
				}
			}

			if !allowed {
				httputil.WriteError(w, exception.Forbidden("insufficient permissions"))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// GetUserID retrieves the user ID from the request context.
// Returns empty string if not found.
func GetUserID(ctx context.Context) string {
	if id, ok := ctx.Value(userContextKey{}).(string); ok {
		return id
	}
	return ""
}

// GetUserRole retrieves the user role from the request context.
// Returns empty string if not found.
func GetUserRole(ctx context.Context) string {
	if role, ok := ctx.Value(roleContextKey{}).(string); ok {
		return role
	}
	return ""
}

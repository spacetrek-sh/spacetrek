// Package jwt provides JWT token generation and validation utilities.
package jwt

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	// ErrInvalidToken is returned when token validation fails.
	ErrInvalidToken = errors.New("invalid token")
	// ErrTokenExpired is returned when token has expired.
	ErrTokenExpired = errors.New("token expired")
)

// Claims represents the custom JWT claims for user authentication.
type Claims struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

// Manager handles JWT token generation and validation.
type Manager struct {
	secretKey    []byte
	accessTokenT time.Duration
}

// NewManager creates a new JWT manager with the given secret and access token duration.
func NewManager(secret string, accessTokenT time.Duration) *Manager {
	return &Manager{
		secretKey:    []byte(secret),
		accessTokenT: accessTokenT,
	}
}

// GenerateAccessToken creates a new JWT access token for the given user.
func (m *Manager) GenerateAccessToken(userID, role string) (string, time.Time, error) {
	now := time.Now()
	expiresAt := now.Add(m.accessTokenT)

	claims := Claims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(m.secretKey)
	if err != nil {
		return "", time.Time{}, err
	}

	return tokenString, expiresAt, nil
}

// ValidateToken validates the given JWT token string and returns the claims.
func (m *Manager) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		// Validate signing method
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return m.secretKey, nil
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}

	return claims, nil
}

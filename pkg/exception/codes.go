package exception

import (
	"fmt"
	"net/http"
)

// FieldError represents a single field-level validation failure.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error code constants — machine-readable identifiers returned in API responses.
const (
	CodeNotFound           = "NOT_FOUND"
	CodeUnauthorized       = "UNAUTHORIZED"
	CodeForbidden          = "FORBIDDEN"
	CodeBadRequest         = "BAD_REQUEST"
	CodeConflict           = "CONFLICT"
	CodeValidationError    = "VALIDATION_ERROR"
	CodeUnprocessable      = "UNPROCESSABLE_ENTITY"
	CodeInternalError      = "INTERNAL_SERVER_ERROR"
	CodeServiceUnavailable = "SERVICE_UNAVAILABLE"
	CodeTooManyRequests    = "TOO_MANY_REQUESTS"
	CodeTimeout            = "TIMEOUT"
)

// NotFound returns a 404 error for a missing resource.
func NotFound(resource, id string) *AppError {
	return &AppError{
		Code:       CodeNotFound,
		Message:    fmt.Sprintf("%s %q not found", resource, id),
		StatusCode: http.StatusNotFound,
	}
}

// Unauthorized returns a 401 error. msg defaults to "authentication required".
func Unauthorized(msg string) *AppError {
	if msg == "" {
		msg = "authentication required"
	}
	return &AppError{Code: CodeUnauthorized, Message: msg, StatusCode: http.StatusUnauthorized}
}

// Forbidden returns a 403 error. msg defaults to "access denied".
func Forbidden(msg string) *AppError {
	if msg == "" {
		msg = "access denied"
	}
	return &AppError{Code: CodeForbidden, Message: msg, StatusCode: http.StatusForbidden}
}

// BadRequest returns a 400 error.
func BadRequest(msg string) *AppError {
	return &AppError{Code: CodeBadRequest, Message: msg, StatusCode: http.StatusBadRequest}
}

// Conflict returns a 409 error.
func Conflict(msg string) *AppError {
	return &AppError{Code: CodeConflict, Message: msg, StatusCode: http.StatusConflict}
}

// ValidationFailed returns a 422 error carrying per-field validation failures.
func ValidationFailed(fields []FieldError) *AppError {
	return &AppError{
		Code:       CodeValidationError,
		Message:    "request validation failed",
		StatusCode: http.StatusUnprocessableEntity,
		Details:    fields,
	}
}

// Internal returns a 500 error wrapping an underlying error.
// The original error message is intentionally hidden from the caller.
func Internal(err error) *AppError {
	return &AppError{
		Code:       CodeInternalError,
		Message:    "an unexpected error occurred",
		StatusCode: http.StatusInternalServerError,
		Err:        err,
	}
}

// ServiceUnavailable returns a 503 error.
func ServiceUnavailable(msg string) *AppError {
	if msg == "" {
		msg = "service temporarily unavailable"
	}
	return &AppError{Code: CodeServiceUnavailable, Message: msg, StatusCode: http.StatusServiceUnavailable}
}

// TooManyRequests returns a 429 error.
func TooManyRequests(msg string) *AppError {
	if msg == "" {
		msg = "rate limit exceeded"
	}
	return &AppError{Code: CodeTooManyRequests, Message: msg, StatusCode: http.StatusTooManyRequests}
}

package exception

import (
	"errors"
	"fmt"
)

// AppError is a custom error type for the application that includes an HTTP status code.
type AppError struct {
	Code       string       // A machine-readable error code (e.g., "NOT_FOUND")
	Message    string       // A human-readable message
	StatusCode int          // The HTTP status code to be returned to the client
	Details    []FieldError // Optional field-level validation errors
	Err        error        // The original underlying error (for wrapping)
}

// Error returns the string representation of the error, satisfying the error interface.
func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

// Unwrap provides compatibility for Go's errors.Is and errors.As.
func (e *AppError) Unwrap() error {
	return e.Err
}

// New creates a new AppError with a status code, without wrapping an underlying error.
func New(code, message string, statusCode int) *AppError {
	return &AppError{
		Code:       code,
		Message:    message,
		StatusCode: statusCode,
	}
}

// Wrap creates a new AppError that wraps an underlying error.
func Wrap(code, message string, statusCode int, err error) *AppError {
	return &AppError{
		Code:       code,
		Message:    message,
		StatusCode: statusCode,
		Err:        err,
	}
}

// FromError converts any error into an *AppError.
// If the error is already an *AppError it is returned as-is.
// Otherwise a generic 500 InternalError is returned.
func FromError(err error) *AppError {
	if err == nil {
		return nil
	}
	var appErr *AppError
	if errors.As(err, &appErr) {
		return appErr
	}
	return Internal(err)
}

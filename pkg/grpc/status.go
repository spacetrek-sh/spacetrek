// Package grpcutil provides helpers for translating domain errors to and from
// gRPC status codes.
package grpcutil

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
)

// FromAppError converts an *exception.AppError to a gRPC *status.Status.
// If err is nil, it returns a nil status.
func FromAppError(err *exception.AppError) error {
	if err == nil {
		return nil
	}
	return status.Error(toGRPCCode(err.Code), err.Message)
}

// ToAppError converts a gRPC status error back into an *exception.AppError.
// Non-status errors are wrapped as Internal.
func ToAppError(err error) *exception.AppError {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		return exception.Internal(err)
	}
	httpStatus := toHTTPStatus(st.Code())
	return exception.New(toErrorCode(st.Code()), st.Message(), httpStatus)
}

func toGRPCCode(appCode string) codes.Code {
	switch appCode {
	case exception.CodeNotFound:
		return codes.NotFound
	case exception.CodeUnauthorized:
		return codes.Unauthenticated
	case exception.CodeForbidden:
		return codes.PermissionDenied
	case exception.CodeBadRequest, exception.CodeValidationError, exception.CodeUnprocessable:
		return codes.InvalidArgument
	case exception.CodeConflict:
		return codes.AlreadyExists
	case exception.CodeTooManyRequests:
		return codes.ResourceExhausted
	case exception.CodeTimeout:
		return codes.DeadlineExceeded
	case exception.CodeServiceUnavailable:
		return codes.Unavailable
	default:
		return codes.Internal
	}
}

func toErrorCode(c codes.Code) string {
	switch c {
	case codes.NotFound:
		return exception.CodeNotFound
	case codes.Unauthenticated:
		return exception.CodeUnauthorized
	case codes.PermissionDenied:
		return exception.CodeForbidden
	case codes.InvalidArgument:
		return exception.CodeBadRequest
	case codes.AlreadyExists:
		return exception.CodeConflict
	case codes.ResourceExhausted:
		return exception.CodeTooManyRequests
	case codes.DeadlineExceeded:
		return exception.CodeTimeout
	case codes.Unavailable:
		return exception.CodeServiceUnavailable
	default:
		return exception.CodeInternalError
	}
}

func toHTTPStatus(c codes.Code) int {
	switch c {
	case codes.NotFound:
		return 404
	case codes.Unauthenticated:
		return 401
	case codes.PermissionDenied:
		return 403
	case codes.InvalidArgument:
		return 400
	case codes.AlreadyExists:
		return 409
	case codes.ResourceExhausted:
		return 429
	case codes.DeadlineExceeded:
		return 408
	case codes.Unavailable:
		return 503
	default:
		return 500
	}
}

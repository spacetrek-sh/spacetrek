// Package grpcmiddleware provides gRPC unary and stream interceptors for
// logging, panic recovery, and request validation.
package grpcmiddleware

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	grpcutil "github.com/spacetrek-sh/spacetrek/pkg/grpc"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
)

// requestIDMetaKey is the gRPC metadata key used to propagate request IDs.
const requestIDMetaKey = "x-request-id"

// Validatable is implemented by protobuf message types (or hand-written
// request structs) that want their fields checked before the handler runs.
type Validatable interface {
	Validate() error
}

// ---------------------------------------------------------------------------
// Unary interceptors
// ---------------------------------------------------------------------------

// UnaryPathValidation rejects any RPC whose FullMethod does not start with a
// leading slash. This prevents HTTP/2 :path pseudo-header bypass attacks
// against path-based authorization interceptors (e.g. grpc/authz).
func UnaryPathValidation() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if info.FullMethod == "" || info.FullMethod[0] != '/' {
			return nil, status.Errorf(codes.Unimplemented, "malformed method name")
		}
		return handler(ctx, req)
	}
}

// UnaryLogging returns a unary interceptor that attaches a request-scoped
// logger to the context and logs each RPC with its duration and status code.
func UnaryLogging(base *slog.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		start := time.Now()
		reqID := requestIDFromMeta(ctx)

		reqLogger := base.With(
			slog.String("grpc.method", info.FullMethod),
			slog.String("request_id", reqID),
		)
		ctx = pkglog.WithLogger(ctx, reqLogger)

		resp, err := handler(ctx, req)

		code := codes.OK
		if err != nil {
			code = status.Code(err)
		}

		reqLogger.InfoContext(ctx, "rpc completed",
			slog.String("grpc.code", code.String()),
			slog.Duration("duration", time.Since(start)),
		)
		return resp, err
	}
}

// UnaryRecovery returns a unary interceptor that catches panics and converts
// them to gRPC Internal errors so the server keeps running.
func UnaryRecovery() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp any, err error) {
		defer func() {
			if rec := recover(); rec != nil {
				logger := pkglog.FromContext(ctx)

				var panicErr error
				switch v := rec.(type) {
				case error:
					panicErr = v
				default:
					panicErr = fmt.Errorf("%v", v)
				}

				logger.ErrorContext(ctx, "grpc panic recovered",
					"panic", panicErr,
					"method", info.FullMethod,
				)
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()
		return handler(ctx, req)
	}
}

// UnaryValidation returns a unary interceptor that calls Validate() on the
// incoming request if it implements the Validatable interface.
// Validation errors are translated to gRPC InvalidArgument status errors.
func UnaryValidation() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if v, ok := req.(Validatable); ok {
			if err := v.Validate(); err != nil {
				return nil, grpcutil.FromAppError(exception.FromError(err))
			}
		}
		return handler(ctx, req)
	}
}

// ---------------------------------------------------------------------------
// Stream interceptors
// ---------------------------------------------------------------------------

// StreamLogging returns a stream interceptor equivalent of UnaryLogging.
func StreamLogging(base *slog.Logger) grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		start := time.Now()
		reqID := requestIDFromMeta(ss.Context())

		reqLogger := base.With(
			slog.String("grpc.method", info.FullMethod),
			slog.String("request_id", reqID),
			slog.Bool("client_stream", info.IsClientStream),
			slog.Bool("server_stream", info.IsServerStream),
		)

		ctx := pkglog.WithLogger(ss.Context(), reqLogger)
		wrapped := &wrappedStream{ServerStream: ss, ctx: ctx}

		err := handler(srv, wrapped)

		code := codes.OK
		if err != nil {
			code = status.Code(err)
		}
		reqLogger.InfoContext(ctx, "stream rpc completed",
			slog.String("grpc.code", code.String()),
			slog.Duration("duration", time.Since(start)),
		)
		return err
	}
}

// StreamPathValidation is the stream equivalent of UnaryPathValidation.
func StreamPathValidation() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if info.FullMethod == "" || info.FullMethod[0] != '/' {
			return status.Errorf(codes.Unimplemented, "malformed method name")
		}
		return handler(srv, ss)
	}
}

// StreamRecovery returns a stream interceptor that catches panics.
func StreamRecovery() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) (err error) {
		defer func() {
			if rec := recover(); rec != nil {
				logger := pkglog.FromContext(ss.Context())

				var panicErr error
				switch v := rec.(type) {
				case error:
					panicErr = v
				default:
					panicErr = fmt.Errorf("%v", v)
				}

				logger.ErrorContext(ss.Context(), "grpc stream panic recovered",
					"panic", panicErr,
					"method", info.FullMethod,
				)
				err = status.Errorf(codes.Internal, "internal server error")
			}
		}()
		return handler(srv, ss)
	}
}

// ---------------------------------------------------------------------------
// ServerOptions is a convenience constructor returning all standard interceptors
// pre-configured and ready to pass to grpc.NewServer.
//
//	srv := grpc.NewServer(grpcmiddleware.ServerOptions(logger)...)
//
// ---------------------------------------------------------------------------
func ServerOptions(logger *slog.Logger) []grpc.ServerOption {
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(
			UnaryPathValidation(),
			UnaryLogging(logger),
			UnaryRecovery(),
			UnaryValidation(),
		),
		grpc.ChainStreamInterceptor(
			StreamPathValidation(),
			StreamLogging(logger),
			StreamRecovery(),
		),
	}
}

// ---------------------------------------------------------------------------
// internal helpers
// ---------------------------------------------------------------------------

// wrappedStream overrides Context() so downstream handlers see the enriched
// context that carries the request-scoped logger.
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

// requestIDFromMeta extracts the x-request-id value from incoming gRPC metadata.
func requestIDFromMeta(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if vals := md.Get(requestIDMetaKey); len(vals) > 0 {
		return vals[0]
	}
	return ""
}

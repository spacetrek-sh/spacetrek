package log

import (
	"context"
	"log/slog"
)

type contextKey struct{}

// WithLogger attaches a *slog.Logger to a context. Subsequent calls to
// FromContext will retrieve this logger instead of the default.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, l)
}

// FromContext retrieves the *slog.Logger stored in ctx.
// If no logger is present it falls back to slog.Default().
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

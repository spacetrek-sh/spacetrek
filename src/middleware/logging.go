package middleware

import (
	"log/slog"
	"net/http"
	"time"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
)

// responseWriter wraps http.ResponseWriter to capture the status code written
// by downstream handlers.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{ResponseWriter: w, status: http.StatusOK}
}

func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Logging is an HTTP middleware that:
//  1. Attaches a request-scoped logger (with request_id, method, path) to the
//     context via pkglog.WithLogger — downstream handlers retrieve it with
//     pkglog.FromContext(ctx).
//  2. Logs each completed request at INFO level with duration and status code.
func Logging(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			reqLogger := base.With(
				slog.String("request_id", GetRequestID(r.Context())),
				slog.String("correlation_id", GetCorrelationID(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("remote_addr", r.RemoteAddr),
			)

			ctx := pkglog.WithLogger(r.Context(), reqLogger)
			rw := newResponseWriter(w)

			next.ServeHTTP(rw, r.WithContext(ctx))

			reqLogger.InfoContext(ctx, "request completed",
				slog.Int("status", rw.status),
				slog.Duration("duration", time.Since(start)),
			)
		})
	}
}

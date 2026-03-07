package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

type correlationIDKey struct{}

const correlationIDHeader = "X-Correlation-ID"

// CorrelationID is an HTTP middleware that ensures every request carries a
// correlation ID for distributed tracing across services. It reads the
// X-Correlation-ID header from the incoming request (typically propagated by
// upstream services or API gateways) and generates a fresh UUID v4 when absent.
// The ID is stored in the request context and echoed back in the response header.
func CorrelationID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(correlationIDHeader)
		if id == "" {
			id = uuid.NewString()
		}

		w.Header().Set(correlationIDHeader, id)
		ctx := context.WithValue(r.Context(), correlationIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetCorrelationID retrieves the correlation ID stored in the context by the
// CorrelationID middleware. Returns an empty string when none is present.
func GetCorrelationID(ctx context.Context) string {
	if id, ok := ctx.Value(correlationIDKey{}).(string); ok {
		return id
	}
	return ""
}

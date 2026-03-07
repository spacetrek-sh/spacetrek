package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

type requestIDKey struct{}

const requestIDHeader = "X-Request-ID"

// RequestID is an HTTP middleware that ensures every request carries a unique
// request ID.  It first checks the incoming X-Request-ID header (set by load
// balancers / API gateways) and falls back to a freshly generated UUID v4.
// The ID is stored in the request context and echoed back in the response
// header so clients can correlate logs.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" {
			id = uuid.NewString()
		}

		w.Header().Set(requestIDHeader, id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetRequestID retrieves the request ID stored in the context by the RequestID
// middleware. Returns an empty string when none is present.
func GetRequestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

package middleware

import (
	"fmt"
	"net/http"

	"github.com/kumori-sh/spacetrk/pkg/exception"
	httputil "github.com/kumori-sh/spacetrk/pkg/http"
	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
)

// Recovery is an HTTP middleware that catches panics in downstream handlers,
// logs the recovered value together with the request-scoped logger, and
// returns a 500 Internal Server Error JSON response so the server stays up.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logger := pkglog.FromContext(r.Context())

				var err error
				switch v := rec.(type) {
				case error:
					err = v
				default:
					err = fmt.Errorf("%v", v)
				}

				logger.ErrorContext(r.Context(), "panic recovered",
					"panic", err,
					"request_id", GetRequestID(r.Context()),
				)

				httputil.WriteError(w, exception.Internal(err))
			}
		}()

		next.ServeHTTP(w, r)
	})
}

// Package intra wires the localhost-bound HTTP server that exposes
// service operations to sibling containers sharing the orchestrator's
// network namespace (notably spacetrek-activator). It is intentionally
// stripped down compared to src/api/http: no CORS, no auth, no public
// exposure. Bind to 127.0.0.1 only — never to a publicly reachable
// interface.
package intra

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	httputil "github.com/spacetrek-sh/spacetrek/pkg/http"
	"github.com/spacetrek-sh/spacetrek/src/middleware"
	internsvm "github.com/spacetrek-sh/spacetrek/src/api/http/intra/vm"
)

// Config holds everything the internal HTTP server needs to start.
type Config struct {
	Addr      string // must be 127.0.0.1:port
	Logger    *slog.Logger
	VMHandler *internsvm.Handler
}

// NewServer builds the localhost-bound *http.Server. The middleware chain
// is the same shape as the public server (CorrelationID → RequestID →
// Logging → Recovery) minus CORS and auth.
func NewServer(cfg Config) *http.Server {
	r := chi.NewRouter()

	r.Get("/internal/v1/health", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteJSON(w, http.StatusOK, "healthy", map[string]string{"status": "ok"})
	})

	r.Route("/internal/v1", func(r chi.Router) {
		if cfg.VMHandler != nil {
			cfg.VMHandler.RegisterRoutes(r)
		}
	})

	handler := middleware.CorrelationID(
		middleware.RequestID(
			middleware.Logging(cfg.Logger)(
				middleware.Recovery(r),
			),
		),
	)

	return &http.Server{
		Addr:         cfg.Addr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 300 * time.Second, // cold-starts can take a minute
		IdleTimeout:  60 * time.Second,
	}
}

// Package apihttp wires the HTTP server, routes, and middleware chain.
package apihttp

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	httputil "github.com/kumori-sh/spacetrk/pkg/http"
	agenthttp "github.com/kumori-sh/spacetrk/src/api/http/v1/agent"
	authhttp "github.com/kumori-sh/spacetrk/src/api/http/v1/auth"
	sessionhttp "github.com/kumori-sh/spacetrk/src/api/http/v1/session"
	"github.com/kumori-sh/spacetrk/src/middleware"
)

// Config holds everything the HTTP server needs to start.
type Config struct {
	Addr           string
	Logger         *slog.Logger
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	IdleTimeout    time.Duration
	AgentHandler   *agenthttp.Handler
	SessionHandler *sessionhttp.Handler
	AuthHandler    *authhttp.Handler
}

// New builds and returns a configured *http.Server with all routes and the
// standard middleware chain applied. Callers are responsible for calling
// ListenAndServe / Shutdown.
func New(cfg Config) *http.Server {
	r := chi.NewRouter()
	registerRoutes(r, cfg)

	// Middleware chain (outermost = first to execute):
	//   CorrelationID  →  RequestID  →  Logging  →  Recovery  →  chi router
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
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}
}

func registerRoutes(r chi.Router, cfg Config) {
	// ── Health ────────────────────────────────────────────────────────────
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		httputil.WriteJSON(w, http.StatusOK, "healthy", map[string]string{"status": "ok"})
	})

	// ── API v1 ────────────────────────────────────────────────────────────
	r.Route("/api/v1", func(r chi.Router) {
		cfg.AuthHandler.RegisterRoutes(r)
		cfg.AgentHandler.RegisterRoutes(r)
		cfg.SessionHandler.RegisterRoutes(r)
	})
}

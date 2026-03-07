package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	apihttp "github.com/kumori-sh/spacetrk/src/api/http"
	agenthttp "github.com/kumori-sh/spacetrk/src/api/http/v1/agent"
	sessionhttp "github.com/kumori-sh/spacetrk/src/api/http/v1/session"
	"github.com/kumori-sh/spacetrk/src/repository/memory"
	agentsvc "github.com/kumori-sh/spacetrk/src/service/agent"
	sessionsvc "github.com/kumori-sh/spacetrk/src/service/session"
)

func main() {
	// ── Logger ────────────────────────────────────────────────────────────
	logger := pkglog.New(pkglog.DefaultConfig())
	pkglog.SetAsDefault(logger)

	// ── Repositories (in-memory; swap for Postgres in production) ─────────
	agentRepo := memory.NewAgentRepository()
	sessionRepo := memory.NewSessionRepository()

	// ── Services ──────────────────────────────────────────────────────────
	agentService := agentsvc.New(agentRepo)
	sessionService := sessionsvc.New(sessionRepo, agentRepo)

	// ── Handlers ──────────────────────────────────────────────────────────
	agentHandler := agenthttp.NewHandler(agentService)
	sessionHandler := sessionhttp.NewHandler(sessionService)

	// ── HTTP Server ───────────────────────────────────────────────────────
	addr := envOr("HTTP_ADDR", ":8080")
	srv := apihttp.New(apihttp.Config{
		Addr:           addr,
		Logger:         logger,
		ReadTimeout:    15 * time.Second,
		WriteTimeout:   30 * time.Second,
		IdleTimeout:    60 * time.Second,
		AgentHandler:   agentHandler,
		SessionHandler: sessionHandler,
	})

	// ── Graceful Shutdown ─────────────────────────────────────────────────
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("server started", slog.String("addr", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", slog.Any("error", err))
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", slog.Any("error", err))
	}
	logger.Info("server stopped")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kumori-sh/spacetrk/pkg/auth/jwt"
	"github.com/kumori-sh/spacetrk/pkg/config"
	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	apihttp "github.com/kumori-sh/spacetrk/src/api/http"
	agenthttp "github.com/kumori-sh/spacetrk/src/api/http/v1/agent"
	authhttp "github.com/kumori-sh/spacetrk/src/api/http/v1/auth"
	sessionhttp "github.com/kumori-sh/spacetrk/src/api/http/v1/session"
	"github.com/kumori-sh/spacetrk/src/repository/memory"
	postgresrepo "github.com/kumori-sh/spacetrk/src/repository/postgres"
	authservice "github.com/kumori-sh/spacetrk/src/service/auth"
	agentsvc "github.com/kumori-sh/spacetrk/src/service/agent"
	sessionsvc "github.com/kumori-sh/spacetrk/src/service/session"
	usersvc "github.com/kumori-sh/spacetrk/src/service/user"
)

func main() {
	// ── Config ────────────────────────────────────────────────────────────
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", slog.Any("error", err))
		os.Exit(1)
	}

	// ── Logger ────────────────────────────────────────────────────────────
	logger := pkglog.New(pkglog.DefaultConfig())
	pkglog.SetAsDefault(logger)

	// ── Database ──────────────────────────────────────────────────────────
	db, err := postgresrepo.Connect(context.Background(), cfg.Database.URL, cfg.Database.MaxConnections)
	if err != nil {
		slog.Error("failed to connect to database", slog.Any("error", err))
		os.Exit(1)
	}
	defer db.Close()

	// ── Repositories ───────────────────────────────────────────────────────
	agentRepo := memory.NewAgentRepository()
	sessionRepo := memory.NewSessionRepository()
	userRepo := postgresrepo.NewUserRepository(db)
	authRepo := postgresrepo.NewAuthRepository(db)

	// ── JWT Manager ────────────────────────────────────────────────────────
	jwtManager := jwt.NewManager(cfg.Security.JWTSecret, cfg.Security.AccessTokenExpiry)

	// ── Services ────────────────────────────────────────────────────────────
	agentService := agentsvc.New(agentRepo)
	sessionService := sessionsvc.New(sessionRepo, agentRepo)
	userService := usersvc.NewService(userRepo)
	authService := authservice.NewService(jwtManager, authRepo, userRepo)

	// ── Handlers ────────────────────────────────────────────────────────────
	agentHandler := agenthttp.NewHandler(agentService)
	sessionHandler := sessionhttp.NewHandler(sessionService)
	authHandler := authhttp.NewHandler(userService, authService, jwtManager)

	// ── HTTP Server ───────────────────────────────────────────────────────
	srv := apihttp.New(apihttp.Config{
		Addr:           cfg.Server.HTTPAddr,
		Logger:         logger,
		ReadTimeout:    cfg.Server.ReadTimeout,
		WriteTimeout:   cfg.Server.WriteTimeout,
		IdleTimeout:    cfg.Server.IdleTimeout,
		AgentHandler:   agentHandler,
		SessionHandler: sessionHandler,
		AuthHandler:    authHandler,
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

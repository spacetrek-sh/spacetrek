package main

import (
	"context"
	"fmt"
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
	vmhttp "github.com/kumori-sh/spacetrk/src/api/http/v1/vm"
	vmdomain "github.com/kumori-sh/spacetrk/src/core/domain/vm"
	"github.com/kumori-sh/spacetrk/src/infrastructure/vm/firecracker"
	"github.com/kumori-sh/spacetrk/src/repository/memory"
	postgresrepo "github.com/kumori-sh/spacetrk/src/repository/postgres"
	agentsvc "github.com/kumori-sh/spacetrk/src/service/agent"
	authservice "github.com/kumori-sh/spacetrk/src/service/auth"
	sessionsvc "github.com/kumori-sh/spacetrk/src/service/session"
	usersvc "github.com/kumori-sh/spacetrk/src/service/user"
	vmsvc "github.com/kumori-sh/spacetrk/src/service/vm"
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
	environmentRepo := postgresrepo.NewEnvironmentRepository(db)
	vmRepo := memory.NewVMRepository()
	userRepo := postgresrepo.NewUserRepository(db)
	authRepo := postgresrepo.NewAuthRepository(db)

	// ── JWT Manager ────────────────────────────────────────────────────────
	jwtManager := jwt.NewManager(cfg.Security.JWTSecret, cfg.Security.AccessTokenExpiry)

	// ── Services ────────────────────────────────────────────────────────────
	agentService := agentsvc.New(agentRepo)
	sessionService := sessionsvc.New(sessionRepo, agentRepo)
	userService := usersvc.NewService(userRepo)
	authService := authservice.NewService(jwtManager, authRepo, userRepo)

	fcCfg := firecracker.Config{
		BinaryPath:      cfg.VM.Firecracker.BinaryPath,
		KernelPath:      cfg.VM.Firecracker.KernelPath,
		BaseDir:         cfg.VM.Firecracker.BaseDir,
		KernelArgs:      cfg.VM.Firecracker.KernelArgs,
		MacAddress:      cfg.VM.Firecracker.MacAddress,
		SocketTimeout:   cfg.VM.Firecracker.SocketTimeout,
		ShutdownTimeout: cfg.VM.Firecracker.ShutdownTimeout,
		SMT:             cfg.VM.Firecracker.SMT,
		EnableMmds:      cfg.VM.Firecracker.EnableMmds,
	}
	var vmBackend vmdomain.Backend
	provider, err := firecracker.NewProvider(fcCfg)
	if err != nil {
		logger.Warn("firecracker backend unavailable, VM APIs will return backend unavailable errors", slog.Any("error", err))
		vmBackend = unavailableBackend{reason: err.Error()}
	} else {
		vmBackend = provider
	}

	vmService := vmsvc.NewService(vmRepo, vmBackend, environmentRepo)

	// ── Handlers ────────────────────────────────────────────────────────────
	agentHandler := agenthttp.NewHandler(agentService)
	sessionHandler := sessionhttp.NewHandler(sessionService)
	authHandler := authhttp.NewHandler(userService, authService, jwtManager)
	vmHandler := vmhttp.NewHandler(vmService, jwtManager, environmentRepo)

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
		VMHandler:      vmHandler,
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

type unavailableBackend struct {
	reason string
}

func (b unavailableBackend) Create(context.Context, vmdomain.CreateSpec) (string, error) {
	return "", b.err()
}

func (b unavailableBackend) Start(context.Context, string) error {
	return b.err()
}

func (b unavailableBackend) Stop(context.Context, string) error {
	return b.err()
}

func (b unavailableBackend) Destroy(context.Context, string) error {
	return b.err()
}

func (b unavailableBackend) Status(context.Context, string) (vmdomain.RuntimeStatus, error) {
	return vmdomain.RuntimeStatus{}, b.err()
}

func (b unavailableBackend) Execute(context.Context, string, []string) (string, string, int, error) {
	return "", "", -1, b.err()
}

func (b unavailableBackend) GetMetrics(context.Context, string) (vmdomain.Metrics, error) {
	return vmdomain.Metrics{}, b.err()
}

func (b unavailableBackend) err() error {
	return fmt.Errorf("vm backend unavailable: %s", b.reason)
}

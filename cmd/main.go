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
	chathttp "github.com/kumori-sh/spacetrk/src/api/http/v1/chat"
	vmhttp "github.com/kumori-sh/spacetrk/src/api/http/v1/vm"
	vmdomain "github.com/kumori-sh/spacetrk/src/core/domain/vm"
	"github.com/kumori-sh/spacetrk/src/core/ports"
	geminiadapter "github.com/kumori-sh/spacetrk/src/infrastructure/llm/gemini"
	"github.com/kumori-sh/spacetrk/src/infrastructure/vm/firecracker"
	s3storage "github.com/kumori-sh/spacetrk/src/infrastructure/storage/s3"
	postgresrepo "github.com/kumori-sh/spacetrk/src/repository/postgres"
	agentsvc "github.com/kumori-sh/spacetrk/src/service/agent"
	authservice "github.com/kumori-sh/spacetrk/src/service/auth"
	orchestratorsvc "github.com/kumori-sh/spacetrk/src/service/orchestrator"
	chatsvc "github.com/kumori-sh/spacetrk/src/service/chat"
	toolsvc "github.com/kumori-sh/spacetrk/src/service/tool"
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
	logCfg := pkglog.DefaultConfig()
	logCfg.Level = cfg.Log.Level
	logger := pkglog.New(logCfg)
	pkglog.SetAsDefault(logger)

	// ── Database ──────────────────────────────────────────────────────────
	db, err := postgresrepo.Connect(context.Background(), cfg.Database.URL, cfg.Database.MaxConnections)
	if err != nil {
		slog.Error("failed to connect to database", slog.Any("error", err))
		os.Exit(1)
	}
	defer db.Close()

	// ── Repositories ───────────────────────────────────────────────────────
	agentRepo := postgresrepo.NewAgentRepository(db)
	chatRepo := postgresrepo.NewChatRepository(db)
	environmentRepo := postgresrepo.NewEnvironmentRepository(db)
	vmRepo := postgresrepo.NewVMRepository(db)
	vmMetricsHistoryRepo := postgresrepo.NewVMMetricsHistoryRepository(db)
	snapRepo := postgresrepo.NewSnapshotRepository(db)
	userRepo := postgresrepo.NewUserRepository(db)
	authRepo := postgresrepo.NewAuthRepository(db)

	// ── JWT Manager ────────────────────────────────────────────────────────
	jwtManager := jwt.NewManager(cfg.Security.JWTSecret, cfg.Security.AccessTokenExpiry)

	// ── Services ────────────────────────────────────────────────────────────
	agentService := agentsvc.New(agentRepo)
	userService := usersvc.NewService(userRepo)
	authService := authservice.NewService(jwtManager, authRepo, userRepo)

	fcCfg := firecracker.Config{
		BinaryPath:         cfg.VM.Firecracker.BinaryPath,
		KernelPath:         cfg.VM.Firecracker.KernelPath,
		BaseDir:            cfg.VM.Firecracker.BaseDir,
		KernelArgs:         cfg.VM.Firecracker.KernelArgs,
		MacAddress:         cfg.VM.Firecracker.MacAddress,
		SocketTimeout:      cfg.VM.Firecracker.SocketTimeout,
		ShutdownTimeout:    cfg.VM.Firecracker.ShutdownTimeout,
		SMT:                cfg.VM.Firecracker.SMT,
		EnableMmds:         cfg.VM.Firecracker.EnableMmds,
		ExecEnabled:        cfg.VM.Firecracker.ExecEnabled,
		GuestAgentPort:     cfg.VM.Firecracker.GuestAgentPort,
		VsockSocketName:    cfg.VM.Firecracker.VsockSocketName,
		CIDMin:             cfg.VM.Firecracker.CIDMin,
		CIDMax:             cfg.VM.Firecracker.CIDMax,
		DefaultExecTimeout: cfg.VM.Firecracker.DefaultExecTimeout,
		MaxStdoutBytes:     cfg.VM.Firecracker.MaxStdoutBytes,
		MaxStderrBytes:     cfg.VM.Firecracker.MaxStderrBytes,
	}
	var vmBackend vmdomain.Backend
	provider, err := firecracker.NewProvider(fcCfg)
	if err != nil {
		logger.Warn("firecracker backend unavailable, VM APIs will return backend unavailable errors", slog.Any("error", err))
		vmBackend = unavailableBackend{reason: err.Error()}
	} else {
		vmBackend = provider
	}

	// ── Snapshot Storage ─────────────────────────────────────────────────────
	var snapshotStore ports.SnapshotStore
	if cfg.Storage.Endpoint != "" {
		ss, err := s3storage.NewStore(context.Background(), s3storage.Config{
			Endpoint:     cfg.Storage.Endpoint,
			Region:       cfg.Storage.Region,
			AccessKey:    cfg.Storage.AccessKey,
			SecretKey:    cfg.Storage.SecretKey,
			Bucket:       cfg.Storage.Bucket,
			UsePathStyle: cfg.Storage.UsePathStyle,
		})
		if err != nil {
			logger.Warn("S3 snapshot store unavailable, snapshots will be stored locally", slog.Any("error", err))
		} else {
			if err := ss.EnsureBucket(context.Background()); err != nil {
				logger.Warn("Failed to ensure S3 bucket", slog.Any("error", err))
			}
			snapshotStore = ss
			logger.Info("S3 snapshot store configured", slog.String("bucket", cfg.Storage.Bucket))
		}
	}

	vmService := vmsvc.NewService(vmRepo, vmMetricsHistoryRepo, vmBackend, environmentRepo, snapRepo, snapshotStore, cfg.VM.IdleTimeout, cfg.VM.AutoSnapshot, cfg.VM.ResumeGrace)
	orchTools := orchestratorsvc.NewInMemoryToolRegistry(nil)
	orchTools.Register(toolsvc.NewVMCommandTool(vmService))
	orchTools.Register(toolsvc.NewVMCreateTool(vmService))
	orchTools.Register(toolsvc.NewVMStartTool(vmService))
	orchTools.Register(toolsvc.NewVMListTool(vmService))
	orchTools.Register(toolsvc.NewVMStopTool(vmService))
	orchTools.Register(toolsvc.NewVMSnapshotTool(vmService))

	var planner ports.ToolPlanner
	if cfg.LLM.DefaultProvider == "gemini" && cfg.LLM.Gemini.APIKey != "" {
		geminiCfg := geminiadapter.Config{
			APIKey:          cfg.LLM.Gemini.APIKey,
			Model:           cfg.LLM.Gemini.Model,
			MaxOutputTokens: int32(cfg.LLM.Gemini.MaxOutputTokens),
			SystemPrompt:    cfg.LLM.Gemini.SystemPrompt,
			Timeout:         cfg.LLM.Timeout,
		}
		if geminiCfg.Model == "" {
			geminiCfg.Model = geminiadapter.DefaultConfig().Model
		}
		if geminiCfg.MaxOutputTokens == 0 {
			geminiCfg.MaxOutputTokens = geminiadapter.DefaultConfig().MaxOutputTokens
		}
		gp, err := geminiadapter.NewPlanner(context.Background(), geminiCfg, orchTools)
		if err != nil {
			logger.Warn("gemini planner unavailable, falling back to rule planner", slog.Any("error", err))
			planner = orchestratorsvc.NewRulePlanner()
		} else {
			planner = gp
			logger.Info("using gemini planner", slog.String("model", geminiCfg.Model))
		}
	} else {
		planner = orchestratorsvc.NewRulePlanner()
		logger.Info("using rule-based planner (no gemini config)")
	}

	orchService := orchestratorsvc.NewWithConfig(
		planner,
		orchTools,
		orchestratorsvc.NewMemoryStateStore(),
		orchestratorsvc.NewConfig([]string{"vm.execute_command", "vm.create", "vm.start", "vm.list", "vm.stop", "vm.snapshot"}, cfg.Security.MaxTaskDuration),
	)
	chatService := chatsvc.New(chatRepo, agentRepo, orchService)

	// ── Handlers ────────────────────────────────────────────────────────────
	agentHandler := agenthttp.NewHandler(agentService, jwtManager)
	chatHandler := chathttp.NewHandler(chatService, jwtManager)
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
		ChatHandler:   chatHandler,
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

	go vmService.StartIdleReaper(pkglog.WithLogger(ctx, logger), time.Minute)
	go vmService.StartRuntimeReconciler(pkglog.WithLogger(ctx, logger), 30*time.Second)
	go vmService.StartMetricsCollector(pkglog.WithLogger(ctx, logger), 10*time.Second)

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

func (b unavailableBackend) CreateSnapshot(context.Context, string) (string, int64, error) {
	return "", 0, b.err()
}

func (b unavailableBackend) RestoreFromSnapshot(context.Context, vmdomain.CreateSpec, string) (string, error) {
	return "", b.err()
}

func (b unavailableBackend) StopPreserving(context.Context, string) error {
	return b.err()
}

func (b unavailableBackend) err() error {
	return fmt.Errorf("vm backend unavailable: %s", b.reason)
}

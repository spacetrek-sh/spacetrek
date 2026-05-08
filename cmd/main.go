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

	"github.com/spacetrek-sh/spacetrek/pkg/auth/jwt"
	"github.com/spacetrek-sh/spacetrek/pkg/config"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	apihttp "github.com/spacetrek-sh/spacetrek/src/api/http"
	agenthttp "github.com/spacetrek-sh/spacetrek/src/api/http/v1/agent"
	authhttp "github.com/spacetrek-sh/spacetrek/src/api/http/v1/auth"
	chathttp "github.com/spacetrek-sh/spacetrek/src/api/http/v1/chat"
	vmhttp "github.com/spacetrek-sh/spacetrek/src/api/http/v1/vm"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
	"github.com/spacetrek-sh/spacetrek/src/core/ports"
	geminiadapter "github.com/spacetrek-sh/spacetrek/src/infrastructure/llm/gemini"
	"github.com/spacetrek-sh/spacetrek/src/infrastructure/vm/firecracker"
	s3storage "github.com/spacetrek-sh/spacetrek/src/infrastructure/storage/s3"
	postgresrepo "github.com/spacetrek-sh/spacetrek/src/repository/postgres"
	agentsvc "github.com/spacetrek-sh/spacetrek/src/service/agent"
	authservice "github.com/spacetrek-sh/spacetrek/src/service/auth"
	orchestratorsvc "github.com/spacetrek-sh/spacetrek/src/service/orchestrator"
	chatsvc "github.com/spacetrek-sh/spacetrek/src/service/chat"
	toolsvc "github.com/spacetrek-sh/spacetrek/src/service/tool"
	usersvc "github.com/spacetrek-sh/spacetrek/src/service/user"
	vmsvc "github.com/spacetrek-sh/spacetrek/src/service/vm"
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
	snapMetricsRepo := postgresrepo.NewSnapshotMetricsRepository(db)
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

	// Pass network config to firecracker provider when enabled.
	if cfg.VM.NetworkEnabled && cfg.VM.Firecracker.Network.BridgeName != "" {
		fcCfg.Network = firecracker.NetworkConfig{
			BridgeName: cfg.VM.Firecracker.Network.BridgeName,
			Subnet:     cfg.VM.Firecracker.Network.Subnet,
			GatewayIP:  cfg.VM.Firecracker.Network.GatewayIP,
			DNSIP:      cfg.VM.Firecracker.Network.DNSIP,
			IPStart:    cfg.VM.Firecracker.Network.IPStart,
			IPEnd:      cfg.VM.Firecracker.Network.IPEnd,
			EnableNAT:  cfg.VM.Firecracker.Network.EnableNAT,
		}
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

	// Build IP allocator when networking is enabled.
	var vmIPAllocator *vmsvc.IPAllocator
	var vmNetworkCfg vmsvc.NetworkConfig
	if cfg.VM.NetworkEnabled && cfg.VM.Firecracker.Network.BridgeName != "" {
		var allocErr error
		vmIPAllocator, allocErr = vmsvc.NewIPAllocator(vmRepo, cfg.VM.Firecracker.Network.IPStart, cfg.VM.Firecracker.Network.IPEnd)
		if allocErr != nil {
			logger.Warn("failed to create IP allocator, networking disabled", slog.Any("error", allocErr))
			vmIPAllocator = nil
		} else {
			vmNetworkCfg = vmsvc.NetworkConfig{
				BridgeName: cfg.VM.Firecracker.Network.BridgeName,
				Subnet:     cfg.VM.Firecracker.Network.Subnet,
				GatewayIP:  cfg.VM.Firecracker.Network.GatewayIP,
				DNSIP:      cfg.VM.Firecracker.Network.DNSIP,
			}
			logger.Info("VM networking enabled", slog.String("bridge", vmNetworkCfg.BridgeName), slog.String("subnet", vmNetworkCfg.Subnet))
		}
	}

	vmService := vmsvc.NewService(vmRepo, vmMetricsHistoryRepo, vmBackend, environmentRepo, snapRepo, snapMetricsRepo, snapshotStore, cfg.VM.IdleTimeout, cfg.VM.AutoSnapshot, cfg.VM.ResumeGrace, vmNetworkCfg, vmIPAllocator)
	orchTools := orchestratorsvc.NewInMemoryToolRegistry(nil)
	orchTools.Register(toolsvc.NewVMCommandTool(vmService))
	orchTools.Register(toolsvc.NewVMCreateTool(vmService))
	orchTools.Register(toolsvc.NewVMStartTool(vmService))
	orchTools.Register(toolsvc.NewVMListTool(vmService))
	orchTools.Register(toolsvc.NewVMStopTool(vmService))
	orchTools.Register(toolsvc.NewVMSnapshotTool(vmService))
	orchTools.Register(toolsvc.NewVMReadFileTool(vmService))
	orchTools.Register(toolsvc.NewVMWriteFileTool(vmService))
	orchTools.Register(toolsvc.NewVMEditFileTool(vmService))

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

	maxReactSteps := cfg.LLM.MaxReactSteps
	if maxReactSteps <= 0 {
		maxReactSteps = 30
	}
	orchService := orchestratorsvc.NewWithConfig(
		planner,
		orchTools,
		orchestratorsvc.NewMemoryStateStore(),
		orchestratorsvc.NewConfig([]string{"vm.execute_command", "vm.create", "vm.start", "vm.list", "vm.stop", "vm.snapshot", "vm.read_file", "vm.write_file", "vm.edit_file"}, cfg.Security.MaxTaskDuration, maxReactSteps),
	)
	runtimeEventRepo := postgresrepo.NewRuntimeEventRepository(db)
	vmCollector := chatsvc.NewAvailableVMCollector(vmService, environmentRepo)
	chatService := chatsvc.New(chatRepo, runtimeEventRepo, agentRepo, orchService, vmCollector)

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

func (b unavailableBackend) CreateSnapshot(context.Context, string) (*vmdomain.SnapshotResult, error) {
	return nil, b.err()
}

func (b unavailableBackend) RestoreFromSnapshot(context.Context, vmdomain.CreateSpec, string) (string, error) {
	return "", b.err()
}

func (b unavailableBackend) StopPreserving(context.Context, string) error {
	return b.err()
}

func (b unavailableBackend) ReadFile(context.Context, string, string, int, int) (string, error) {
	return "", b.err()
}

func (b unavailableBackend) WriteFile(context.Context, string, string, string, int) error {
	return b.err()
}

func (b unavailableBackend) EditFile(context.Context, string, string, string, string, bool) error {
	return b.err()
}

func (b unavailableBackend) err() error {
	return fmt.Errorf("vm backend unavailable: %s", b.reason)
}

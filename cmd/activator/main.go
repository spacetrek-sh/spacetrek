// Package main is the spacetrek-activator binary. It runs as a sibling
// container to spacetrek-api with network_mode: "service:spacetrek-api",
// giving it direct access to the VM mesh (10.200.0.0/16) and the
// orchestrator's localhost-bound internal API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/activator"
)

func main() {
	logCfg := pkglog.DefaultConfig()
	logCfg.Level = "info"
	logger := pkglog.New(logCfg)
	pkglog.SetAsDefault(logger)

	cfg := loadConfig(logger)

	orch := activator.NewOrchestratorClient(cfg.orchestratorURL)
	act := activator.NewActivator(orch, cfg.maxConcurrent)
	handler := activator.NewServer(activator.Config{
		Mode:               cfg.mode,
		DomainSuffix:       cfg.domainSuffix,
		OrchestratorClient: orch,
		Activator:          act,
		Logger:             logger,
		ColdStartBudget:    cfg.coldStart,
	})

	srv := &http.Server{
		Addr:         cfg.listenAddr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: cfg.coldStart + 30*time.Second, // cover cold-start + the actual request
		IdleTimeout:  60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("activator started",
			slog.String("addr", cfg.listenAddr),
			slog.String("mode", string(cfg.mode)),
			slog.String("orchestrator", cfg.orchestratorURL),
			slog.String("domain_suffix", cfg.domainSuffix),
			slog.Int("max_concurrent_activations", cfg.maxConcurrent),
			slog.Duration("cold_start_budget", cfg.coldStart))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("activator server error", slog.Any("error", err))
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("activator shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("activator shutdown error", slog.Any("error", err))
	}
	logger.Info("activator stopped")
}

type runtimeConfig struct {
	listenAddr      string
	mode            activator.Mode
	orchestratorURL string
	domainSuffix    string
	maxConcurrent   int
	coldStart       time.Duration
}

func loadConfig(logger *slog.Logger) runtimeConfig {
	cfg := runtimeConfig{
		listenAddr:      env("ACTIVATOR_LISTEN_ADDR", ":8090"),
		mode:            activator.Mode(env("ACTIVATOR_MODE", string(activator.ModeCloudflared))),
		orchestratorURL: env("ORCHESTRATOR_INTERNAL_URL", "http://localhost:8081"),
		domainSuffix:    env("DOMAIN_SUFFIX", ".box.spacetrek.xyz"),
		maxConcurrent:   envInt("MAX_CONCURRENT_ACTIVATIONS", 5, logger),
		coldStart:       envDuration("COLD_START_BUDGET", 60*time.Second, logger),
	}
	return cfg
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int, logger *slog.Logger) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		logger.Warn("invalid int env; using default", "key", key, "value", v, "default", def)
		return def
	}
	return n
}

func envDuration(key string, def time.Duration, logger *slog.Logger) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		logger.Warn("invalid duration env; using default", "key", key, "value", v, "default", def)
		return def
	}
	return d
}

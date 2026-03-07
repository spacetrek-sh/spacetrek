package log

import (
	"log/slog"
	"os"
	"time"

	"github.com/lmittmann/tint"
)

type Config struct {
	Level      string // Log level (debug, info, warn, error)
	TimeFormat string // Time format for logs
	AddSource  bool   // Whether to add source file information
}

func DefaultConfig() *Config {
	return &Config{
		Level:      "info",
		TimeFormat: time.RFC3339,
		AddSource:  true,
	}
}

func New(cfg *Config) *slog.Logger {
	var logLevel slog.Level
	if err := logLevel.UnmarshalText([]byte(cfg.Level)); err != nil {
		logLevel = slog.LevelInfo
		slog.Warn("Invalid LOG_LEVEL specified, defaulting to INFO", "provided_level", cfg.Level)
	}

	logHandler := newHandler(cfg)
	return slog.New(logHandler)
}

func newHandler(cfg *Config) slog.Handler {
	var logLevel slog.Level
	if err := logLevel.UnmarshalText([]byte(cfg.Level)); err != nil {
		logLevel = slog.LevelInfo
	}

	// Check if we're in production mode
	isProduction := cfg.Level == "production" || os.Getenv("ENVIRONMENT") == "production"

	if isProduction {
		// Use JSON handler for production
		return slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level:     logLevel,
			AddSource: cfg.AddSource,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				// Customize output format if needed
				return a
			},
		})
	}

	// Use tint (colored) handler for development
	return tint.NewHandler(os.Stdout, &tint.Options{
		Level:      logLevel,
		TimeFormat: cfg.TimeFormat,
		AddSource:  cfg.AddSource,
	})
}

func SetAsDefault(logger *slog.Logger) {
	slog.SetDefault(logger)
}

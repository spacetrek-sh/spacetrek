package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/google/uuid"
	"github.com/spacetrek-sh/spacetrek/pkg/config"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	postgresrepo "github.com/spacetrek-sh/spacetrek/src/repository/postgres"
)

const defaultSeedFile = "seeds/environments.json"

type environmentSeed struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	ImagePath   string `json:"image_path"`
	VCPU        int    `json:"vcpu"`
	MemoryMB    int    `json:"memory_mb"`
	DiskMB      int    `json:"disk_mb"`
	Description string `json:"description"`
}

func main() {
	seedFile := flag.String("file", defaultSeedFile, "path to seed file (.json)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", slog.Any("error", err))
		os.Exit(1)
	}

	logger := pkglog.New(pkglog.DefaultConfig())
	pkglog.SetAsDefault(logger)

	ctx := context.Background()
	db, err := postgresrepo.Connect(ctx, cfg.Database.URL, cfg.Database.MaxConnections)
	if err != nil {
		logger.Error("failed to connect to database", slog.Any("error", err))
		os.Exit(1)
	}
	defer db.Close()

	if err := seedFromJSON(ctx, db, cfg, *seedFile); err != nil {
		logger.Error("failed to seed database from json", slog.Any("error", err), slog.String("file", *seedFile))
		os.Exit(1)
	}
	logger.Info("database seeded from json", "file", *seedFile)
}

func seedFromJSON(ctx context.Context, db *postgresrepo.DB, cfg *config.Config, filePath string) error {
	content, err := os.ReadFile(filePath) // #nosec G304 -- operator-supplied seed file path
	if err != nil {
		return fmt.Errorf("read seed file: %w", err)
	}

	var rows []environmentSeed
	if err := json.Unmarshal(content, &rows); err != nil {
		return fmt.Errorf("decode json seed: %w", err)
	}

	if len(rows) == 0 {
		return fmt.Errorf("json seed file contains no rows: %s", filePath)
	}

	return upsertEnvironments(ctx, db, cfg, rows)
}

func upsertEnvironments(ctx context.Context, db *postgresrepo.DB, cfg *config.Config, rows []environmentSeed) error {
	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	ns := uuid.MustParse(cfg.Seed.NamespaceUUID)
	queryByID := `
		INSERT INTO environments (id, type, image_path, resource_limits, description, metadata)
		VALUES ($1, $2::environment_type, $3, jsonb_build_object('vcpu', $4::int, 'memory_mb', $5::int, 'disk_mb', $6::int), $7, NULL)
		ON CONFLICT (id) DO UPDATE SET
			type = EXCLUDED.type,
			image_path = EXCLUDED.image_path,
			resource_limits = EXCLUDED.resource_limits,
			description = EXCLUDED.description,
			updated_at = NOW()
	`

	for _, row := range rows {
		if row.Type == "" || row.ImagePath == "" {
			return fmt.Errorf("invalid seed row: type and image_path are required")
		}

		id := row.ID
		if id == "" {
			id = uuid.NewSHA1(ns, []byte("environment:"+row.Type)).String()
		}

		if _, err := tx.ExecContext(ctx, queryByID, id, row.Type, row.ImagePath, row.VCPU, row.MemoryMB, row.DiskMB, row.Description); err != nil {
			return fmt.Errorf("upsert environment %q: %w", row.Type, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit seed transaction: %w", err)
	}

	return nil
}

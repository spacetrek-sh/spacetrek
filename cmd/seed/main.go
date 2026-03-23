package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/kumori-sh/spacetrk/pkg/config"
	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	postgresrepo "github.com/kumori-sh/spacetrk/src/repository/postgres"
)

const defaultSeedFile = "seeds/environments.json"

type environmentSeed struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	ImagePath string `json:"image_path"`
	VCPU      int    `json:"vcpu"`
	MemoryMB  int    `json:"memory_mb"`
	DiskMB    int    `json:"disk_mb"`
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

	if err := seedFromJSON(ctx, db, *seedFile); err != nil {
		logger.Error("failed to seed database from json", slog.Any("error", err), slog.String("file", *seedFile))
		os.Exit(1)
	}
	logger.Info("database seeded from json", "file", *seedFile)
}

func seedFromJSON(ctx context.Context, db *postgresrepo.DB, filePath string) error {
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

	return upsertEnvironments(ctx, db, rows)
}

func upsertEnvironments(ctx context.Context, db *postgresrepo.DB, rows []environmentSeed) error {
	queryByID := `
		INSERT INTO environments (id, type, image_path, resource_limits, metadata)
		VALUES ($1, $2::environment_type, $3, jsonb_build_object('vcpu', $4::int, 'memory_mb', $5::int, 'disk_mb', $6::int), NULL)
		ON CONFLICT (id) DO UPDATE SET
			type = EXCLUDED.type,
			image_path = EXCLUDED.image_path,
			resource_limits = EXCLUDED.resource_limits,
			updated_at = NOW()
	`

	queryByType := `
		WITH updated AS (
			UPDATE environments
			SET image_path = $2,
				resource_limits = jsonb_build_object('vcpu', $3::int, 'memory_mb', $4::int, 'disk_mb', $5::int),
				updated_at = NOW()
			WHERE type = $1::environment_type
			RETURNING id
		)
		INSERT INTO environments (type, image_path, resource_limits, metadata)
		SELECT $1::environment_type, $2, jsonb_build_object('vcpu', $3::int, 'memory_mb', $4::int, 'disk_mb', $5::int), NULL
		WHERE NOT EXISTS (SELECT 1 FROM updated)
	`

	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	for _, row := range rows {
		if row.Type == "" || row.ImagePath == "" {
			_ = tx.Rollback()
			return fmt.Errorf("invalid seed row: type and image_path are required")
		}

		if row.ID == "" {
			if _, err := tx.ExecContext(ctx, queryByType, row.Type, row.ImagePath, row.VCPU, row.MemoryMB, row.DiskMB); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("upsert environment by type %q: %w", row.Type, err)
			}
			continue
		}

		if _, err := tx.ExecContext(ctx, queryByID, row.ID, row.Type, row.ImagePath, row.VCPU, row.MemoryMB, row.DiskMB); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("upsert environment %q: %w", row.ID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit seed transaction: %w", err)
	}

	return nil
}

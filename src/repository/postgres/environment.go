// Package postgres provides the PostgreSQL implementation of the environment repository.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/environment"
)

type environmentRepository struct {
	db *DB
}

type environmentRow struct {
	ID             string           `db:"id"`
	Type           string           `db:"type"`
	ImagePath      string           `db:"image_path"`
	ResourceLimits json.RawMessage  `db:"resource_limits"`
	Description    string           `db:"description"`
	Metadata       *json.RawMessage `db:"metadata"`
	DiffSnapshots  bool             `db:"diff_snapshots"`
	CreatedAt      sql.NullTime     `db:"created_at"`
	UpdatedAt      sql.NullTime     `db:"updated_at"`
}

// NewEnvironmentRepository creates a new environment repository backed by PostgreSQL.
func NewEnvironmentRepository(db *DB) environment.Repository {
	return &environmentRepository{db: db}
}

func (r *environmentRepository) Create(ctx context.Context, env *environment.Environment) error {
	logger := pkglog.FromContext(ctx)

	resourceLimitsJSON, err := json.Marshal(env.ResourceLimits)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: marshal resource limits failed", "environment_id", env.ID, "error", err)
		return exception.Internal(fmt.Errorf("marshal resource limits: %w", err))
	}

	query := `
		INSERT INTO environments (id, type, image_path, resource_limits, description, metadata, diff_snapshots, created_at, updated_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6::jsonb, $7, $8, $9)
	`

	if _, err := r.db.ExecContext(
		ctx,
		query,
		env.ID,
		string(env.Type),
		env.ImagePath,
		resourceLimitsJSON,
		env.Description,
		env.Metadata,
		env.DiffSnapshots,
		env.CreatedAt,
		env.UpdatedAt,
	); err != nil {
		logger.ErrorContext(ctx, "postgres: create environment failed", "environment_id", env.ID, "error", err)
		return exception.Internal(fmt.Errorf("create environment: %w", err))
	}

	return nil
}

func (r *environmentRepository) GetByID(ctx context.Context, id string) (*environment.Environment, error) {
	logger := pkglog.FromContext(ctx)

	query := `
		SELECT id, type, image_path, resource_limits, description, metadata, diff_snapshots, created_at, updated_at
		FROM environments
		WHERE id = $1
	`

	var row environmentRow
	if err := r.db.GetContext(ctx, &row, query, id); err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("environment", id)
		}
		logger.ErrorContext(ctx, "postgres: get environment by id failed", "environment_id", id, "error", err)
		return nil, exception.Internal(fmt.Errorf("get environment by id: %w", err))
	}

	return mapEnvironmentRow(row)
}

func (r *environmentRepository) List(ctx context.Context) ([]*environment.Environment, error) {
	logger := pkglog.FromContext(ctx)

	query := `
		SELECT id, type, image_path, resource_limits, description, metadata, diff_snapshots, created_at, updated_at
		FROM environments
		ORDER BY created_at DESC
	`

	rows := make([]environmentRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query); err != nil {
		logger.ErrorContext(ctx, "postgres: list environments failed", "error", err)
		return nil, exception.Internal(fmt.Errorf("list environments: %w", err))
	}

	out := make([]*environment.Environment, 0, len(rows))
	for _, row := range rows {
		env, err := mapEnvironmentRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, env)
	}

	return out, nil
}

func (r *environmentRepository) Update(ctx context.Context, env *environment.Environment) error {
	logger := pkglog.FromContext(ctx)

	resourceLimitsJSON, err := json.Marshal(env.ResourceLimits)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: marshal resource limits failed", "environment_id", env.ID, "error", err)
		return exception.Internal(fmt.Errorf("marshal resource limits: %w", err))
	}

	query := `
		UPDATE environments
		SET type = $2,
		    image_path = $3,
		    resource_limits = $4::jsonb,
		    description = $5,
		    metadata = $6::jsonb,
		    diff_snapshots = $7,
		    updated_at = $8
		WHERE id = $1
	`

	result, err := r.db.ExecContext(
		ctx,
		query,
		env.ID,
		string(env.Type),
		env.ImagePath,
		resourceLimitsJSON,
		env.Description,
		env.Metadata,
		env.DiffSnapshots,
		env.UpdatedAt,
	)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: update environment failed", "environment_id", env.ID, "error", err)
		return exception.Internal(fmt.Errorf("update environment: %w", err))
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		logger.ErrorContext(ctx, "postgres: update environment rows affected failed", "environment_id", env.ID, "error", err)
		return exception.Internal(fmt.Errorf("update environment rows affected: %w", err))
	}

	if rowsAffected == 0 {
		return exception.NotFound("environment", env.ID)
	}

	return nil
}

func (r *environmentRepository) Delete(ctx context.Context, id string) error {
	logger := pkglog.FromContext(ctx)

	query := `DELETE FROM environments WHERE id = $1`

	result, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: delete environment failed", "environment_id", id, "error", err)
		return exception.Internal(fmt.Errorf("delete environment: %w", err))
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		logger.ErrorContext(ctx, "postgres: delete environment rows affected failed", "environment_id", id, "error", err)
		return exception.Internal(fmt.Errorf("delete environment rows affected: %w", err))
	}

	if rowsAffected == 0 {
		return exception.NotFound("environment", id)
	}

	return nil
}

func mapEnvironmentRow(row environmentRow) (*environment.Environment, error) {
	var resourceLimits environment.ResourceLimits
	if err := json.Unmarshal(row.ResourceLimits, &resourceLimits); err != nil {
		return nil, exception.Internal(fmt.Errorf("decode environment resource limits: %w", err))
	}

	env := &environment.Environment{
		ID:             row.ID,
		Type:           environment.Type(row.Type),
		ImagePath:      row.ImagePath,
		ResourceLimits: resourceLimits,
		Description:    row.Description,
		Metadata:       row.Metadata,
		DiffSnapshots:  row.DiffSnapshots,
	}

	if row.CreatedAt.Valid {
		env.CreatedAt = row.CreatedAt.Time
	}
	if row.UpdatedAt.Valid {
		env.UpdatedAt = row.UpdatedAt.Time
	}

	return env, nil
}

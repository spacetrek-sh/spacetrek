// Package postgres provides the PostgreSQL implementation of the environment repository.
package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/kumori-sh/spacetrk/pkg/exception"
	"github.com/kumori-sh/spacetrk/src/core/domain/environment"
)

type environmentRepository struct {
	db *DB
}

type environmentRow struct {
	ID             string           `db:"id"`
	Type           string           `db:"type"`
	ImagePath      string           `db:"image_path"`
	ResourceLimits json.RawMessage  `db:"resource_limits"`
	Metadata       *json.RawMessage `db:"metadata"`
	CreatedAt      sql.NullTime     `db:"created_at"`
	UpdatedAt      sql.NullTime     `db:"updated_at"`
}

// NewEnvironmentRepository creates a new environment repository backed by PostgreSQL.
func NewEnvironmentRepository(db *DB) environment.Repository {
	return &environmentRepository{db: db}
}

func (r *environmentRepository) Create(ctx context.Context, env *environment.Environment) error {
	resourceLimitsJSON, err := json.Marshal(env.ResourceLimits)
	if err != nil {
		return exception.Internal(fmt.Errorf("marshal resource limits: %w", err))
	}

	query := `
		INSERT INTO environments (id, type, image_path, resource_limits, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, $6, $7)
	`

	if _, err := r.db.ExecContext(
		ctx,
		query,
		env.ID,
		string(env.Type),
		env.ImagePath,
		resourceLimitsJSON,
		env.Metadata,
		env.CreatedAt,
		env.UpdatedAt,
	); err != nil {
		return exception.Internal(fmt.Errorf("create environment: %w", err))
	}

	return nil
}

func (r *environmentRepository) GetByID(ctx context.Context, id string) (*environment.Environment, error) {
	query := `
		SELECT id, type, image_path, resource_limits, metadata, created_at, updated_at
		FROM environments
		WHERE id = $1
	`

	var row environmentRow
	if err := r.db.GetContext(ctx, &row, query, id); err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("environment", id)
		}
		return nil, exception.Internal(fmt.Errorf("get environment by id: %w", err))
	}

	return mapEnvironmentRow(row)
}

func (r *environmentRepository) List(ctx context.Context) ([]*environment.Environment, error) {
	query := `
		SELECT id, type, image_path, resource_limits, metadata, created_at, updated_at
		FROM environments
		ORDER BY created_at DESC
	`

	rows := make([]environmentRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query); err != nil {
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
	resourceLimitsJSON, err := json.Marshal(env.ResourceLimits)
	if err != nil {
		return exception.Internal(fmt.Errorf("marshal resource limits: %w", err))
	}

	query := `
		UPDATE environments
		SET type = $2,
		    image_path = $3,
		    resource_limits = $4::jsonb,
		    metadata = $5::jsonb,
		    updated_at = $6
		WHERE id = $1
	`

	result, err := r.db.ExecContext(
		ctx,
		query,
		env.ID,
		string(env.Type),
		env.ImagePath,
		resourceLimitsJSON,
		env.Metadata,
		env.UpdatedAt,
	)
	if err != nil {
		return exception.Internal(fmt.Errorf("update environment: %w", err))
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return exception.Internal(fmt.Errorf("update environment rows affected: %w", err))
	}

	if rowsAffected == 0 {
		return exception.NotFound("environment", env.ID)
	}

	return nil
}

func (r *environmentRepository) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM environments WHERE id = $1`

	result, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		return exception.Internal(fmt.Errorf("delete environment: %w", err))
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
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
		Metadata:       row.Metadata,
	}

	if row.CreatedAt.Valid {
		env.CreatedAt = row.CreatedAt.Time
	}
	if row.UpdatedAt.Valid {
		env.UpdatedAt = row.UpdatedAt.Time
	}

	return env, nil
}

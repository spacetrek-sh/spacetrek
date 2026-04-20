package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/agent"
)

type agentRepository struct {
	db *DB
}

type agentRow struct {
	ID           string         `db:"id"`
	UserID       string         `db:"user_id"`
	Name         string         `db:"name"`
	Description  sql.NullString `db:"description"`
	Model        string         `db:"model"`
	SystemPrompt sql.NullString `db:"system_prompt"`
	Status       string         `db:"status"`
	CreatedAt    sql.NullTime   `db:"created_at"`
	UpdatedAt    sql.NullTime   `db:"updated_at"`
}

func NewAgentRepository(db *DB) agent.Repository {
	return &agentRepository{db: db}
}

func (r *agentRepository) Create(ctx context.Context, a *agent.Agent) error {
	logger := pkglog.FromContext(ctx)

	query := `
		INSERT INTO agents (id, user_id, name, description, model, system_prompt, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`

	if _, err := r.db.ExecContext(ctx, query,
		a.ID, a.UserID, a.Name, toNullString(a.Description),
		a.Model, toNullString(a.SystemPrompt),
		string(a.Status), a.CreatedAt, a.UpdatedAt,
	); err != nil {
		logger.ErrorContext(ctx, "postgres: create agent failed", "agent_id", a.ID, "error", err)
		return exception.Internal(fmt.Errorf("create agent: %w", err))
	}

	return nil
}

func (r *agentRepository) GetByID(ctx context.Context, id string) (*agent.Agent, error) {
	logger := pkglog.FromContext(ctx)

	query := `
		SELECT id, user_id, name, description, model, system_prompt, status, created_at, updated_at
		FROM agents
		WHERE id = $1 AND deleted_at IS NULL
	`

	var row agentRow
	if err := r.db.GetContext(ctx, &row, query, id); err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("agent", id)
		}
		logger.ErrorContext(ctx, "postgres: get agent by id failed", "agent_id", id, "error", err)
		return nil, exception.Internal(fmt.Errorf("get agent by id: %w", err))
	}

	return mapAgentRow(row), nil
}

func (r *agentRepository) List(ctx context.Context, offset, limit int) ([]*agent.Agent, int64, error) {
	logger := pkglog.FromContext(ctx)

	countQuery := `SELECT COUNT(*) FROM agents WHERE deleted_at IS NULL`
	var total int64
	if err := r.db.GetContext(ctx, &total, countQuery); err != nil {
		logger.ErrorContext(ctx, "postgres: count agents failed", "error", err)
		return nil, 0, exception.Internal(fmt.Errorf("count agents: %w", err))
	}

	query := `
		SELECT id, user_id, name, description, model, system_prompt, status, created_at, updated_at
		FROM agents
		WHERE deleted_at IS NULL
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2
	`

	rows := make([]agentRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query, limit, offset); err != nil {
		logger.ErrorContext(ctx, "postgres: list agents failed", "error", err)
		return nil, 0, exception.Internal(fmt.Errorf("list agents: %w", err))
	}

	out := make([]*agent.Agent, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapAgentRow(row))
	}

	return out, total, nil
}

func (r *agentRepository) Update(ctx context.Context, a *agent.Agent) error {
	logger := pkglog.FromContext(ctx)

	query := `
		UPDATE agents
		SET name = $2, description = $3, model = $4, system_prompt = $5,
		    status = $6, updated_at = $7
		WHERE id = $1 AND deleted_at IS NULL
	`

	result, err := r.db.ExecContext(ctx, query,
		a.ID, a.Name, toNullString(a.Description),
		a.Model, toNullString(a.SystemPrompt),
		string(a.Status), a.UpdatedAt,
	)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: update agent failed", "agent_id", a.ID, "error", err)
		return exception.Internal(fmt.Errorf("update agent: %w", err))
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return exception.Internal(fmt.Errorf("update agent rows affected: %w", err))
	}
	if rowsAffected == 0 {
		return exception.NotFound("agent", a.ID)
	}

	return nil
}

func (r *agentRepository) Delete(ctx context.Context, id string) error {
	logger := pkglog.FromContext(ctx)

	query := `UPDATE agents SET deleted_at = NOW() WHERE id = $1 AND deleted_at IS NULL`

	result, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: delete agent failed", "agent_id", id, "error", err)
		return exception.Internal(fmt.Errorf("delete agent: %w", err))
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return exception.Internal(fmt.Errorf("delete agent rows affected: %w", err))
	}
	if rowsAffected == 0 {
		return exception.NotFound("agent", id)
	}

	return nil
}

func mapAgentRow(row agentRow) *agent.Agent {
	a := &agent.Agent{
		ID:     row.ID,
		UserID: row.UserID,
		Name:   row.Name,
		Model:  row.Model,
		Status: agent.Status(row.Status),
	}
	if row.Description.Valid {
		a.Description = row.Description.String
	}
	if row.SystemPrompt.Valid {
		a.SystemPrompt = row.SystemPrompt.String
	}
	if row.CreatedAt.Valid {
		a.CreatedAt = row.CreatedAt.Time
	}
	if row.UpdatedAt.Valid {
		a.UpdatedAt = row.UpdatedAt.Time
	}
	return a
}

func toNullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

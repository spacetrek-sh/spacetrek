package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/agent"
)

type agentMemoryRepository struct {
	db *DB
}

type agentMemoryRow struct {
	ChatID    string       `db:"chat_id"`
	Key       string       `db:"key"`
	Value     string       `db:"value"`
	ExpiresAt sql.NullTime `db:"expires_at"`
	CreatedAt sql.NullTime `db:"created_at"`
	UpdatedAt sql.NullTime `db:"updated_at"`
}

func NewAgentMemoryRepository(db *DB) agent.MemoryRepository {
	return &agentMemoryRepository{db: db}
}

func (r *agentMemoryRepository) Set(ctx context.Context, entry *agent.MemoryEntry) error {
	logger := pkglog.FromContext(ctx)

	var expiresAt any
	if !entry.ExpiresAt.IsZero() {
		expiresAt = entry.ExpiresAt
	}

	query := `
		INSERT INTO agent_memory (chat_id, key, value, expires_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NOW(), NOW())
		ON CONFLICT (chat_id, key) DO UPDATE
		    SET value      = EXCLUDED.value,
		        expires_at = EXCLUDED.expires_at,
		        updated_at = NOW()
	`

	if _, err := r.db.ExecContext(ctx, query, entry.ChatID, entry.Key, entry.Value, expiresAt); err != nil {
		logger.ErrorContext(ctx, "postgres: set agent memory failed",
			"chat_id", entry.ChatID, "key", entry.Key, "error", err)
		return exception.Internal(fmt.Errorf("set agent memory: %w", err))
	}
	return nil
}

func (r *agentMemoryRepository) Get(ctx context.Context, chatID, key string) (*agent.MemoryEntry, error) {
	logger := pkglog.FromContext(ctx)

	query := `
		SELECT chat_id, key, value, expires_at, created_at, updated_at
		FROM agent_memory
		WHERE chat_id = $1 AND key = $2
		  AND (expires_at IS NULL OR expires_at > NOW())
	`

	var row agentMemoryRow
	if err := r.db.GetContext(ctx, &row, query, chatID, key); err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("agent_memory", key)
		}
		logger.ErrorContext(ctx, "postgres: get agent memory failed",
			"chat_id", chatID, "key", key, "error", err)
		return nil, exception.Internal(fmt.Errorf("get agent memory: %w", err))
	}
	return mapAgentMemoryRow(row), nil
}

func (r *agentMemoryRepository) Delete(ctx context.Context, chatID, key string) error {
	logger := pkglog.FromContext(ctx)

	query := `DELETE FROM agent_memory WHERE chat_id = $1 AND key = $2`

	result, err := r.db.ExecContext(ctx, query, chatID, key)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: delete agent memory failed",
			"chat_id", chatID, "key", key, "error", err)
		return exception.Internal(fmt.Errorf("delete agent memory: %w", err))
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return exception.Internal(fmt.Errorf("delete agent memory rows affected: %w", err))
	}
	if rowsAffected == 0 {
		return exception.NotFound("agent_memory", key)
	}
	return nil
}

func (r *agentMemoryRepository) List(ctx context.Context, chatID string) ([]*agent.MemoryEntry, error) {
	logger := pkglog.FromContext(ctx)

	query := `
		SELECT chat_id, key, value, expires_at, created_at, updated_at
		FROM agent_memory
		WHERE chat_id = $1
		  AND (expires_at IS NULL OR expires_at > NOW())
		ORDER BY key ASC
	`

	rows := make([]agentMemoryRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query, chatID); err != nil {
		logger.ErrorContext(ctx, "postgres: list agent memory failed", "chat_id", chatID, "error", err)
		return nil, exception.Internal(fmt.Errorf("list agent memory: %w", err))
	}

	out := make([]*agent.MemoryEntry, 0, len(rows))
	for _, row := range rows {
		out = append(out, mapAgentMemoryRow(row))
	}
	return out, nil
}

func mapAgentMemoryRow(row agentMemoryRow) *agent.MemoryEntry {
	entry := &agent.MemoryEntry{
		ChatID: row.ChatID,
		Key:    row.Key,
		Value:  row.Value,
	}
	if row.ExpiresAt.Valid {
		entry.ExpiresAt = row.ExpiresAt.Time
	}
	if row.CreatedAt.Valid {
		entry.CreatedAt = row.CreatedAt.Time
	}
	if row.UpdatedAt.Valid {
		entry.UpdatedAt = row.UpdatedAt.Time
	}
	return entry
}

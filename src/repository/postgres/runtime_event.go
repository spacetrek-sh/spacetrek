package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	orchdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/orchestrator"
)

type runtimeEventRepository struct {
	db *DB
}

func NewRuntimeEventRepository(db *DB) orchdomain.RuntimeEventRepository {
	return &runtimeEventRepository{db: db}
}

type runtimeEventRow struct {
	ID         string       `db:"id"`
	ChatID     string       `db:"chat_id"`
	TraceID    sql.NullString `db:"trace_id"`
	Type       string       `db:"type"`
	Step       int          `db:"step"`
	Data       string       `db:"data"`
	Command    string       `db:"command"`
	Result     string       `db:"result"`
	Error      string       `db:"error"`
	TokenUsage []byte       `db:"token_usage"`
	Metadata   []byte       `db:"metadata"`
	CreatedAt  sql.NullTime `db:"created_at"`
}

func (r *runtimeEventRepository) Insert(ctx context.Context, event *orchdomain.PersistedRuntimeEvent) error {
	logger := pkglog.FromContext(ctx)

	if event.ID == "" {
		event.ID = uuid.NewString()
	}

	var tokenUsageJSON []byte
	if event.TokenUsage != nil {
		encoded, err := json.Marshal(event.TokenUsage)
		if err != nil {
			return exception.Internal(fmt.Errorf("marshal token_usage: %w", err))
		}
		tokenUsageJSON = encoded
	}

	var metadataJSON []byte
	if len(event.Metadata) > 0 {
		encoded, err := json.Marshal(event.Metadata)
		if err != nil {
			return exception.Internal(fmt.Errorf("marshal event metadata: %w", err))
		}
		metadataJSON = encoded
	}

	query := `
		INSERT INTO runtime_events (id, chat_id, trace_id, type, step, data, command, result, error, token_usage, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`

	if _, err := r.db.ExecContext(ctx, query,
		event.ID, event.ChatID, toNullString(event.TraceID),
		string(event.Type), event.Step, event.Data,
		event.Command, event.Result, event.Error,
		tokenUsageJSON, metadataJSON, event.CreatedAt,
	); err != nil {
		logger.ErrorContext(ctx, "postgres: insert runtime event failed",
			"chat_id", event.ChatID, "type", event.Type, "error", err)
		return exception.Internal(fmt.Errorf("insert runtime event: %w", err))
	}

	return nil
}

func (r *runtimeEventRepository) ListByChatID(ctx context.Context, params orchdomain.ListRuntimeEventsParams) (*orchdomain.ListRuntimeEventsResult, error) {
	logger := pkglog.FromContext(ctx)

	if params.Limit <= 0 || params.Limit > 200 {
		params.Limit = 100
	}

	query := `
		SELECT id, chat_id, trace_id, type, step, data, command, result, error, token_usage, metadata, created_at
		FROM runtime_events
		WHERE chat_id = $1
		  AND ($2::timestamptz IS NULL OR created_at > $2)
		ORDER BY created_at ASC
		LIMIT $3
	`

	var cursorTS any
	if params.Cursor != nil {
		cursorTS = *params.Cursor
	}

	rows := make([]runtimeEventRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query, params.ChatID, cursorTS, params.Limit+1); err != nil {
		logger.ErrorContext(ctx, "postgres: list runtime events failed", "chat_id", params.ChatID, "error", err)
		return nil, exception.Internal(fmt.Errorf("list runtime events: %w", err))
	}

	hasMore := len(rows) > params.Limit
	if hasMore {
		rows = rows[:params.Limit]
	}

	items := make([]*orchdomain.PersistedRuntimeEvent, len(rows))
	for i, row := range rows {
		items[i] = mapRuntimeEventRow(row)
	}

	var nextCursor *time.Time
	if hasMore && len(items) > 0 {
		t := items[len(items)-1].CreatedAt
		nextCursor = &t
	}

	return &orchdomain.ListRuntimeEventsResult{
		Items:      items,
		NextCursor: nextCursor,
	}, nil
}

func (r *runtimeEventRepository) ListRecent(ctx context.Context, limit int) ([]*orchdomain.PersistedRuntimeEvent, error) {
	logger := pkglog.FromContext(ctx)

	if limit <= 0 || limit > 500 {
		limit = 100
	}

	query := `
		SELECT id, chat_id, trace_id, type, step, data, command, result, error, token_usage, metadata, created_at
		FROM runtime_events
		ORDER BY created_at DESC
		LIMIT $1
	`

	rows := make([]runtimeEventRow, 0, limit)
	if err := r.db.SelectContext(ctx, &rows, query, limit); err != nil {
		logger.ErrorContext(ctx, "postgres: list recent runtime events failed", "error", err)
		return nil, exception.Internal(fmt.Errorf("list recent runtime events: %w", err))
	}

	items := make([]*orchdomain.PersistedRuntimeEvent, len(rows))
	for i, row := range rows {
		items[i] = mapRuntimeEventRow(row)
	}

	return items, nil
}

func mapRuntimeEventRow(row runtimeEventRow) *orchdomain.PersistedRuntimeEvent {
	e := &orchdomain.PersistedRuntimeEvent{
		ID:      row.ID,
		ChatID:  row.ChatID,
		Type:    orchdomain.RuntimeEventType(row.Type),
		Step:    row.Step,
		Data:    row.Data,
		Command: row.Command,
		Result:  row.Result,
		Error:   row.Error,
	}
	if row.TraceID.Valid {
		e.TraceID = row.TraceID.String
	}
	if len(row.TokenUsage) > 0 {
		var usage orchdomain.TokenUsage
		if err := json.Unmarshal(row.TokenUsage, &usage); err == nil {
			e.TokenUsage = &usage
		}
	}
	if len(row.Metadata) > 0 {
		var metadata map[string]any
		if err := json.Unmarshal(row.Metadata, &metadata); err == nil {
			e.Metadata = metadata
		}
	}
	if row.CreatedAt.Valid {
		e.CreatedAt = row.CreatedAt.Time
	}
	return e
}

package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/kumori-sh/spacetrk/pkg/exception"
	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	"github.com/kumori-sh/spacetrk/src/core/domain/chat"
)

type chatRepository struct {
	db *DB
}

type chatRow struct {
	ID        string         `db:"id"`
	UserID    string         `db:"user_id"`
	AgentID   string         `db:"agent_id"`
	VMID      sql.NullString `db:"vm_id"`
	Title     string         `db:"title"`
	Status    string         `db:"status"`
	CreatedAt sql.NullTime   `db:"created_at"`
	UpdatedAt sql.NullTime   `db:"updated_at"`
}

type messageRow struct {
	ID             string       `db:"id"`
	ChatID         string       `db:"chat_id"`
	Role           string       `db:"role"`
	ContentBody    string       `db:"content_body"`
	Metadata       []byte       `db:"metadata"`
	SequenceNumber int64        `db:"sequence_number"`
	CreatedAt      sql.NullTime `db:"created_at"`
}

func NewChatRepository(db *DB) chat.Repository {
	return &chatRepository{db: db}
}

func (r *chatRepository) Create(ctx context.Context, c *chat.Chat) error {
	logger := pkglog.FromContext(ctx)

	query := `
		INSERT INTO chats (id, user_id, agent_id, vm_id, title, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`

	if _, err := r.db.ExecContext(ctx, query,
		c.ID, c.UserID, c.AgentID,
		toNullString(c.VMID), c.Title,
		string(c.Status), c.CreatedAt, c.UpdatedAt,
	); err != nil {
		logger.ErrorContext(ctx, "postgres: create chat failed", "chat_id", c.ID, "error", err)
		return exception.Internal(fmt.Errorf("create chat: %w", err))
	}

	if err := r.insertMessages(ctx, c.ID, c.Messages, 0); err != nil {
		return err
	}

	return nil
}

func (r *chatRepository) GetByID(ctx context.Context, id string) (*chat.Chat, error) {
	logger := pkglog.FromContext(ctx)

	query := `
		SELECT id, user_id, agent_id, vm_id, title, status, created_at, updated_at
		FROM chats
		WHERE id = $1 AND deleted_at IS NULL
	`

	var row chatRow
	if err := r.db.GetContext(ctx, &row, query, id); err != nil {
		if err == sql.ErrNoRows {
			return nil, exception.NotFound("chat", id)
		}
		logger.ErrorContext(ctx, "postgres: get chat by id failed", "chat_id", id, "error", err)
		return nil, exception.Internal(fmt.Errorf("get chat by id: %w", err))
	}

	c := mapChatRow(row)

	msgs, err := r.loadMessages(ctx, id)
	if err != nil {
		return nil, err
	}
	c.Messages = msgs

	return c, nil
}

func (r *chatRepository) Update(ctx context.Context, c *chat.Chat) error {
	logger := pkglog.FromContext(ctx)

	query := `
		UPDATE chats
		SET agent_id = $2, vm_id = $3, title = $4, status = $5, updated_at = $6
		WHERE id = $1 AND deleted_at IS NULL
	`

	result, err := r.db.ExecContext(ctx, query,
		c.ID, c.AgentID,
		toNullString(c.VMID), c.Title,
		string(c.Status), c.UpdatedAt,
	)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: update chat failed", "chat_id", c.ID, "error", err)
		return exception.Internal(fmt.Errorf("update chat: %w", err))
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return exception.Internal(fmt.Errorf("update chat rows affected: %w", err))
	}
	if rowsAffected == 0 {
		return exception.NotFound("chat", c.ID)
	}

	// Messages are append-only: find how many exist and insert new ones.
	var existingCount int64
	countQuery := `SELECT COUNT(*) FROM messages WHERE chat_id = $1 AND deleted_at IS NULL`
	if err := r.db.GetContext(ctx, &existingCount, countQuery, c.ID); err != nil {
		logger.ErrorContext(ctx, "postgres: count messages failed", "chat_id", c.ID, "error", err)
		return exception.Internal(fmt.Errorf("count messages: %w", err))
	}

	if existingCount < int64(len(c.Messages)) {
		newMessages := c.Messages[existingCount:]
		if err := r.insertMessages(ctx, c.ID, newMessages, int(existingCount)); err != nil {
			return err
		}
	}

	return nil
}

func (r *chatRepository) Delete(ctx context.Context, id string) error {
	logger := pkglog.FromContext(ctx)

	query := `UPDATE chats SET deleted_at = NOW() WHERE id = $1 AND deleted_at IS NULL`

	result, err := r.db.ExecContext(ctx, query, id)
	if err != nil {
		logger.ErrorContext(ctx, "postgres: delete chat failed", "chat_id", id, "error", err)
		return exception.Internal(fmt.Errorf("delete chat: %w", err))
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return exception.Internal(fmt.Errorf("delete chat rows affected: %w", err))
	}
	if rowsAffected == 0 {
		return exception.NotFound("chat", id)
	}

	return nil
}

func (r *chatRepository) insertMessages(ctx context.Context, chatID string, msgs []chat.Message, startSeq int) error {
	logger := pkglog.FromContext(ctx)

	if len(msgs) == 0 {
		return nil
	}

	query := `
		INSERT INTO messages (id, chat_id, role, content_type, content_body, metadata, sequence_number, created_at)
		VALUES ($1, $2, $3, 'text', $4, $5, $6, $7)
	`

	for i, m := range msgs {
		msgID := uuid.NewString()
		seq := int64(startSeq + i + 1)

		var metadataJSON []byte
		if len(m.Metadata) > 0 {
			encoded, err := json.Marshal(m.Metadata)
			if err != nil {
				return exception.Internal(fmt.Errorf("marshal message metadata: %w", err))
			}
			metadataJSON = encoded
		}

		if _, err := r.db.ExecContext(ctx, query,
			msgID, chatID, string(m.Role), m.Content, metadataJSON, seq, m.At,
		); err != nil {
			logger.ErrorContext(ctx, "postgres: insert message failed",
				"chat_id", chatID, "seq", seq, "error", err)
			return exception.Internal(fmt.Errorf("insert message: %w", err))
		}
	}

	return nil
}

func (r *chatRepository) loadMessages(ctx context.Context, chatID string) ([]chat.Message, error) {
	logger := pkglog.FromContext(ctx)

	query := `
		SELECT id, chat_id, role, content_body, metadata, sequence_number, created_at
		FROM messages
		WHERE chat_id = $1 AND deleted_at IS NULL
		ORDER BY sequence_number ASC
	`

	rows := make([]messageRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query, chatID); err != nil {
		logger.ErrorContext(ctx, "postgres: load messages failed", "chat_id", chatID, "error", err)
		return nil, exception.Internal(fmt.Errorf("load messages: %w", err))
	}

	msgs := make([]chat.Message, 0, len(rows))
	for _, row := range rows {
		m := chat.Message{
			Role:    chat.Role(row.Role),
			Content: row.ContentBody,
		}
		if len(row.Metadata) > 0 {
			var metadata map[string]any
			if err := json.Unmarshal(row.Metadata, &metadata); err != nil {
				return nil, exception.Internal(fmt.Errorf("unmarshal message metadata: %w", err))
			}
			m.Metadata = metadata
		}
		if row.CreatedAt.Valid {
			m.At = row.CreatedAt.Time
		}
		msgs = append(msgs, m)
	}

	return msgs, nil
}

func mapChatRow(row chatRow) *chat.Chat {
	c := &chat.Chat{
		ID:      row.ID,
		UserID:  row.UserID,
		AgentID: row.AgentID,
		Status:  chat.Status(row.Status),
	}
	if row.VMID.Valid {
		c.VMID = row.VMID.String
	}
	if row.Title != "" {
		c.Title = row.Title
	}
	if row.CreatedAt.Valid {
		c.CreatedAt = row.CreatedAt.Time
	}
	if row.UpdatedAt.Valid {
		c.UpdatedAt = row.UpdatedAt.Time
	}
	return c
}

type conversationSummaryRow struct {
	ID            string         `db:"id"`
	UserID        string         `db:"user_id"`
	AgentID       string         `db:"agent_id"`
	VMID          sql.NullString `db:"vm_id"`
	Title         string         `db:"title"`
	Status        string         `db:"status"`
	LastMessage   sql.NullString `db:"last_message"`
	LastMessageAt sql.NullTime   `db:"last_message_at"`
	CreatedAt     sql.NullTime   `db:"created_at"`
	UpdatedAt     sql.NullTime   `db:"updated_at"`
}

func (r *chatRepository) ListByUserID(ctx context.Context, params chat.ListParams) (*chat.ListResult, error) {
	logger := pkglog.FromContext(ctx)

	if params.Limit <= 0 || params.Limit > 100 {
		params.Limit = 20
	}

	query := `
		SELECT c.id, c.user_id, c.agent_id, c.vm_id, c.title, c.status,
		       c.created_at, c.updated_at,
		       lm.content_body AS last_message,
		       lm.created_at   AS last_message_at
		FROM chats c
		LEFT JOIN LATERAL (
		    SELECT content_body, created_at
		    FROM messages
		    WHERE chat_id = c.id AND deleted_at IS NULL
		    ORDER BY sequence_number DESC
		    LIMIT 1
		) lm ON true
		WHERE c.user_id = $1
		  AND c.deleted_at IS NULL
		  AND ($2::timestamptz IS NULL
		       OR c.created_at < $2
		       OR (c.created_at = $2 AND c.id < $3))
		ORDER BY c.created_at DESC, c.id DESC
		LIMIT $4
	`

	var cursorTS any
	var cursorID any
	if params.Cursor != nil {
		cursorTS = params.Cursor.CreatedAt
		cursorID = params.Cursor.ID
	}

	rows := make([]conversationSummaryRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query, params.UserID, cursorTS, cursorID, params.Limit+1); err != nil {
		logger.ErrorContext(ctx, "postgres: list conversations failed", "user_id", params.UserID, "error", err)
		return nil, exception.Internal(fmt.Errorf("list conversations: %w", err))
	}

	hasMore := len(rows) > params.Limit
	if hasMore {
		rows = rows[:params.Limit]
	}

	items := make([]*chat.ConversationSummary, len(rows))
	for i, row := range rows {
		items[i] = mapConversationSummaryRow(row)
	}

	var nextCursor *chat.ListCursor
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		nextCursor = &chat.ListCursor{
			CreatedAt: last.CreatedAt,
			ID:        last.ID,
		}
	}

	logger.DebugContext(ctx, "postgres: listed conversations",
		"user_id", params.UserID, "count", len(items), "has_more", hasMore)

	return &chat.ListResult{
		Items:      items,
		NextCursor: nextCursor,
	}, nil
}

func mapConversationSummaryRow(row conversationSummaryRow) *chat.ConversationSummary {
	s := &chat.ConversationSummary{
		ID:      row.ID,
		UserID:  row.UserID,
		AgentID: row.AgentID,
		Title:   row.Title,
		Status:  chat.Status(row.Status),
	}
	if row.VMID.Valid {
		s.VMID = row.VMID.String
	}
	if row.LastMessage.Valid {
		s.LastMessage = row.LastMessage.String
	}
	if row.LastMessageAt.Valid {
		s.LastMessageAt = row.LastMessageAt.Time
	}
	if row.CreatedAt.Valid {
		s.CreatedAt = row.CreatedAt.Time
	}
	if row.UpdatedAt.Valid {
		s.UpdatedAt = row.UpdatedAt.Time
	}
	return s
}

func (r *chatRepository) ListMessages(ctx context.Context, params chat.ListMessagesParams) (*chat.ListMessagesResult, error) {
	logger := pkglog.FromContext(ctx)

	if params.Limit <= 0 || params.Limit > 100 {
		params.Limit = 50
	}

	query := `
		SELECT id, chat_id, role, content_body, metadata, sequence_number, created_at
		FROM messages
		WHERE chat_id = $1
		  AND deleted_at IS NULL
		  AND ($2::bigint IS NULL OR sequence_number < $2)
		ORDER BY sequence_number DESC
		LIMIT $3
	`

	var cursorSeq any
	if params.Cursor != nil {
		cursorSeq = params.Cursor.SequenceNumber
	}

	rows := make([]messageRow, 0)
	if err := r.db.SelectContext(ctx, &rows, query, params.ChatID, cursorSeq, params.Limit+1); err != nil {
		logger.ErrorContext(ctx, "postgres: list messages failed", "chat_id", params.ChatID, "error", err)
		return nil, exception.Internal(fmt.Errorf("list messages: %w", err))
	}

	hasMore := len(rows) > params.Limit
	if hasMore {
		rows = rows[:params.Limit]
	}

	items := make([]*chat.MessageSummary, len(rows))
	for i, row := range rows {
		m := &chat.MessageSummary{
			ID:             row.ID,
			SequenceNumber: row.SequenceNumber,
			Role:           chat.Role(row.Role),
			Content:        row.ContentBody,
		}
		if len(row.Metadata) > 0 {
			var metadata map[string]any
			if err := json.Unmarshal(row.Metadata, &metadata); err == nil {
				m.Metadata = metadata
			}
		}
		if row.CreatedAt.Valid {
			m.At = row.CreatedAt.Time
		}
		items[i] = m
	}

	var nextCursor *chat.MessageCursor
	if hasMore && len(items) > 0 {
		nextCursor = &chat.MessageCursor{
			SequenceNumber: items[len(items)-1].SequenceNumber,
		}
	}

	logger.DebugContext(ctx, "postgres: listed messages",
		"chat_id", params.ChatID, "count", len(items), "has_more", hasMore)

	return &chat.ListMessagesResult{
		Items:      items,
		NextCursor: nextCursor,
	}, nil
}

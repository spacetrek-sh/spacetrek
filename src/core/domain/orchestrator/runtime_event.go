package orchestrator

import (
	"context"
	"time"
)

// PersistedRuntimeEvent is the database representation of a runtime event.
type PersistedRuntimeEvent struct {
	ID         string           `json:"id"`
	ChatID     string           `json:"chat_id"`
	TraceID    string           `json:"trace_id,omitempty"`
	Type       RuntimeEventType `json:"type"`
	Step       int              `json:"step,omitempty"`
	Data       string           `json:"data,omitempty"`
	Command    string           `json:"command,omitempty"`
	Result     string           `json:"result,omitempty"`
	Error      string           `json:"error,omitempty"`
	TokenUsage *TokenUsage      `json:"token_usage,omitempty"`
	Metadata   map[string]any   `json:"metadata,omitempty"`
	CreatedAt  time.Time        `json:"created_at"`
}

// ListRuntimeEventsParams holds the input for listing runtime events for a chat.
type ListRuntimeEventsParams struct {
	ChatID string
	Cursor *time.Time
	Limit  int
}

// ListRuntimeEventsResult holds a page of runtime events and the next cursor.
type ListRuntimeEventsResult struct {
	Items      []*PersistedRuntimeEvent
	NextCursor *time.Time
}

// RuntimeEventRepository defines the persistence contract for runtime events.
type RuntimeEventRepository interface {
	Insert(ctx context.Context, event *PersistedRuntimeEvent) error
	ListByChatID(ctx context.Context, params ListRuntimeEventsParams) (*ListRuntimeEventsResult, error)
}

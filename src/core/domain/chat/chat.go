package chat

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Status represents the lifecycle state of a Chat.
type Status string

const (
	StatusActive Status = "active"
	StatusIdle   Status = "idle"
	StatusClosed Status = "closed"
)

// Role identifies the author of a message in a conversation.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
)

// Message is a single turn in the conversation history.
type Message struct {
	Role     Role
	Content  string
	Metadata map[string]any
	At       time.Time
}

// Chat is the stateful interaction context between a user and an agent.
type Chat struct {
	ID        string
	AgentID   string
	UserID    string
	Title     string
	VMID      string
	Status    Status
	Messages  []Message
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateParams holds the input required to open a new Chat.
type CreateParams struct {
	AgentID      string
	UserID       string
	AgentName    string
	Model        string
	SystemPrompt string
	Title        string
}

// New constructs a Chat from CreateParams with a generated ID and timestamps.
func New(p CreateParams) *Chat {
	now := time.Now().UTC()
	return &Chat{
		ID:        uuid.NewString(),
		AgentID:   p.AgentID,
		UserID:    p.UserID,
		Status:    StatusActive,
		Messages:  make([]Message, 0),
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// AddMessage appends a new message to the conversation history and updates the
// timestamp.
func (c *Chat) AddMessage(role Role, content string) {
	c.AddMessageWithMetadata(role, content, nil)
}

// AddMessageWithMetadata appends a new message with optional metadata and
// updates the timestamp.
func (c *Chat) AddMessageWithMetadata(role Role, content string, metadata map[string]any) {
	c.Messages = append(c.Messages, Message{
		Role:     role,
		Content:  content,
		Metadata: cloneMetadata(metadata),
		At:       time.Now().UTC(),
	})
	c.UpdatedAt = time.Now().UTC()
}

func cloneMetadata(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	cp := make(map[string]any, len(metadata))
	for k, v := range metadata {
		cp[k] = v
	}
	return cp
}

// ListCursor holds the decoded pagination cursor components.
type ListCursor struct {
	CreatedAt time.Time
	ID        string
}

// ListParams holds the input for listing conversations for a user.
type ListParams struct {
	UserID string
	Cursor *ListCursor // nil means start from the beginning
	Limit  int         // 1–100, default 20
}

// ConversationSummary is a lightweight view of a Chat without messages.
type ConversationSummary struct {
	ID            string
	AgentID       string
	UserID        string
	Title         string
	VMID          string
	Status        Status
	LastMessage   string
	LastMessageAt time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ListResult holds a page of conversation summaries and the next cursor.
type ListResult struct {
	Items      []*ConversationSummary
	NextCursor *ListCursor // nil if no more pages
}

// Repository defines the persistence contract for Chat entities.
type Repository interface {
	Create(ctx context.Context, c *Chat) error
	GetByID(ctx context.Context, id string) (*Chat, error)
	Update(ctx context.Context, c *Chat) error
	Delete(ctx context.Context, id string) error
	ListByUserID(ctx context.Context, params ListParams) (*ListResult, error)
	ListMessages(ctx context.Context, params ListMessagesParams) (*ListMessagesResult, error)
}

// MessageCursor holds the decoded pagination cursor for message listing.
type MessageCursor struct {
	SequenceNumber int64
}

// ListMessagesParams holds the input for listing messages in a chat.
type ListMessagesParams struct {
	ChatID string
	Cursor *MessageCursor // nil = first page (newest)
	Limit  int            // 1–100, default 50
}

// MessageSummary is a single message in a paginated message list.
type MessageSummary struct {
	ID             string
	SequenceNumber int64
	Role           Role
	Content        string
	Metadata       map[string]any
	At             time.Time
}

// ListMessagesResult holds a page of messages and the next cursor.
type ListMessagesResult struct {
	Items      []*MessageSummary
	NextCursor *MessageCursor // nil if no more pages
}

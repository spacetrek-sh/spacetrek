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
	Role    Role
	Content string
	At      time.Time
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
	c.Messages = append(c.Messages, Message{
		Role:    role,
		Content: content,
		At:      time.Now().UTC(),
	})
	c.UpdatedAt = time.Now().UTC()
}

// Repository defines the persistence contract for Chat entities.
type Repository interface {
	Create(ctx context.Context, c *Chat) error
	GetByID(ctx context.Context, id string) (*Chat, error)
	Update(ctx context.Context, c *Chat) error
	Delete(ctx context.Context, id string) error
}

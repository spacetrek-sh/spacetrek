package session

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Status represents the lifecycle state of a Session.
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

// Session is the stateful interaction context between a user and an agent,
// bound to a microVM instance for secure task execution.
type Session struct {
	ID        string
	AgentID   string
	UserID    string
	Status    Status
	Messages  []Message
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CreateParams holds the input required to open a new Session.
type CreateParams struct {
	AgentID string
	UserID  string
}

// New constructs a Session from CreateParams with a generated ID and timestamps.
func New(p CreateParams) *Session {
	now := time.Now().UTC()
	return &Session{
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
// session timestamp.
func (s *Session) AddMessage(role Role, content string) {
	s.Messages = append(s.Messages, Message{
		Role:    role,
		Content: content,
		At:      time.Now().UTC(),
	})
	s.UpdatedAt = time.Now().UTC()
}

// Repository defines the persistence contract for Session entities.
type Repository interface {
	Create(ctx context.Context, s *Session) error
	GetByID(ctx context.Context, id string) (*Session, error)
	Update(ctx context.Context, s *Session) error
	Delete(ctx context.Context, id string) error
}

package agent

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Status represents the lifecycle state of an Agent.
type Status string

const (
	StatusCreated    Status = "created"
	StatusRunning    Status = "running"
	StatusSuspended  Status = "suspended"
	StatusTerminated Status = "terminated"
)

// Agent is the core domain entity for an LLM-powered agent.
type Agent struct {
	ID           string
	Name         string
	Description  string
	Model        string // LLM model identifier (e.g. "gemini-pro", "gpt-4o")
	SystemPrompt string
	Status       Status
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CreateParams holds the input required to create a new Agent.
type CreateParams struct {
	Name         string
	Description  string
	Model        string
	SystemPrompt string
}

// UpdateParams holds the optional fields that can be patched on an Agent.
// A nil pointer means "leave unchanged".
type UpdateParams struct {
	Name         *string
	Description  *string
	Model        *string
	SystemPrompt *string
}

// New constructs an Agent from CreateParams with a generated ID and timestamps.
func New(p CreateParams) *Agent {
	now := time.Now().UTC()
	return &Agent{
		ID:           uuid.NewString(),
		Name:         p.Name,
		Description:  p.Description,
		Model:        p.Model,
		SystemPrompt: p.SystemPrompt,
		Status:       StatusCreated,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

// Repository defines the persistence contract for Agent entities.
// Implementations live in src/repository/.
type Repository interface {
	Create(ctx context.Context, a *Agent) error
	GetByID(ctx context.Context, id string) (*Agent, error)
	List(ctx context.Context, offset, limit int) ([]*Agent, int64, error)
	Update(ctx context.Context, a *Agent) error
	Delete(ctx context.Context, id string) error
}

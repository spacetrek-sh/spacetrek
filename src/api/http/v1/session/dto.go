package sessionhttp

import "time"

// createSessionRequest is the JSON body for POST /api/v1/sessions.
type createSessionRequest struct {
	AgentID      string `json:"agent_id,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	AgentName    string `json:"agent_name,omitempty"`
	Model        string `json:"model,omitempty"`
	SystemPrompt string `json:"system_prompt,omitempty"`
}

// sendMessageRequest is the JSON body for POST /api/v1/sessions/{id}/messages.
type sendMessageRequest struct {
	Content string `json:"content" validate:"required,min=1"`
	VMID    string `json:"vm_id,omitempty"`
}

// messageResponse is the JSON representation of a single conversation turn.
type messageResponse struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	At      time.Time `json:"at"`
}

// sessionResponse is the JSON representation of a session.
type sessionResponse struct {
	ID        string            `json:"id"`
	AgentID   string            `json:"agent_id"`
	UserID    string            `json:"user_id"`
	Status    string            `json:"status"`
	Messages  []messageResponse `json:"messages"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

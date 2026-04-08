package chathttp

import "time"

// sendMessageRequest is the JSON body for POST /api/v1/chat.
type sendMessageRequest struct {
	Message        string `json:"message" validate:"required,min=1"`
	ConversationID string `json:"conversation_id,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	Model          string `json:"model,omitempty"`
	SystemPrompt   string `json:"system_prompt,omitempty"`
}

// messageResponse is the JSON representation of a single conversation turn.
type messageResponse struct {
	Role    string    `json:"role"`
	Content string    `json:"content"`
	At      time.Time `json:"at"`
}

// chatResponse is the JSON representation of a chat.
type chatResponse struct {
	ID        string            `json:"id"`
	AgentID   string            `json:"agent_id"`
	UserID    string            `json:"user_id"`
	Status    string            `json:"status"`
	Messages  []messageResponse `json:"messages"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

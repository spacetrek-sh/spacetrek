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
	Role     string         `json:"role"`
	Content  string         `json:"content"`
	Metadata map[string]any `json:"metadata,omitempty"`
	At       time.Time      `json:"at"`
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

// conversationSummaryResponse is the JSON representation of a conversation list item.
type conversationSummaryResponse struct {
	ID            string    `json:"id"`
	AgentID       string    `json:"agent_id"`
	UserID        string    `json:"user_id"`
	Title         string    `json:"title"`
	VMID          string    `json:"vm_id,omitempty"`
	Status        string    `json:"status"`
	LastMessage   string    `json:"last_message,omitempty"`
	LastMessageAt time.Time `json:"last_message_at,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// listConversationsResponse wraps a paginated list of conversation summaries.
type listConversationsResponse struct {
	Conversations []conversationSummaryResponse `json:"conversations"`
	NextCursor    string                        `json:"next_cursor,omitempty"`
	HasMore       bool                          `json:"has_more"`
}

// messageSummaryResponse is the JSON representation of a message in a paginated list.
type messageSummaryResponse struct {
	ID             string         `json:"id"`
	SequenceNumber int64          `json:"sequence_number"`
	Role           string         `json:"role"`
	Content        string         `json:"content"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	At             time.Time      `json:"at"`
}

// listMessagesResponse wraps a paginated list of messages.
type listMessagesResponse struct {
	Messages   []messageSummaryResponse `json:"messages"`
	NextCursor string                   `json:"next_cursor,omitempty"`
	HasMore    bool                     `json:"has_more"`
}

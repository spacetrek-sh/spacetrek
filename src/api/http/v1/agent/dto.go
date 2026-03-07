package agenthttp

import "time"

// createAgentRequest is the JSON body for POST /api/v1/agents.
type createAgentRequest struct {
	Name         string `json:"name"          validate:"required,min=1,max=100"`
	Description  string `json:"description"   validate:"max=500"`
	Model        string `json:"model"         validate:"required,min=1"`
	SystemPrompt string `json:"system_prompt"`
}

// updateAgentRequest is the JSON body for PUT /api/v1/agents/{id}.
// All fields are optional; absent fields leave the existing value unchanged.
type updateAgentRequest struct {
	Name         *string `json:"name"          validate:"omitempty,min=1,max=100"`
	Description  *string `json:"description"   validate:"omitempty,max=500"`
	Model        *string `json:"model"         validate:"omitempty,min=1"`
	SystemPrompt *string `json:"system_prompt"`
}

// agentResponse is the JSON representation returned for a single agent.
type agentResponse struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Model        string    `json:"model"`
	SystemPrompt string    `json:"system_prompt"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// listAgentsResponse wraps a paginated slice of agents with metadata.
type listAgentsResponse struct {
	Agents []*agentResponse `json:"agents"`
	Total  int64            `json:"total"`
	Offset int              `json:"offset"`
	Limit  int              `json:"limit"`
}

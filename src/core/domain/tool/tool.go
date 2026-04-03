package tool

import "context"

// Parameter describes one argument accepted by a tool.
type Parameter struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// Definition is metadata exposed to the orchestrator and LLM planner.
type Definition struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Parameters  map[string]Parameter `json:"parameters"`
}

// Call describes one tool invocation requested by the planner.
type Call struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// Result is the normalized output returned by tool executors.
type Result struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	OK         bool   `json:"ok"`
	Payload    any    `json:"payload,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Tool defines the domain contract for tool execution.
type Tool interface {
	Definition() Definition
	Execute(ctx context.Context, call Call) (Result, error)
}

package orchestrator

import "time"

// RuntimeEventType is emitted by the runtime orchestrator for client streaming.
type RuntimeEventType string

const (
	EventLLMToken   RuntimeEventType = "llm_token"
	EventToolStart  RuntimeEventType = "tool_start"
	EventToolStdout RuntimeEventType = "tool_stdout"
	EventToolEnd    RuntimeEventType = "tool_end"
	EventAgentError RuntimeEventType = "agent_error"
)

// RuntimeEvent is the transport-neutral runtime streaming payload.
type RuntimeEvent struct {
	Type      RuntimeEventType `json:"type"`
	SessionID string           `json:"session_id,omitempty"`
	ToolName  string           `json:"tool_name,omitempty"`
	Data      string           `json:"data,omitempty"`
	Success   bool             `json:"success,omitempty"`
	Error     string           `json:"error,omitempty"`
	At        time.Time        `json:"at"`
}

package orchestrator

import "time"

// RuntimeEventType is emitted by the runtime orchestrator for client streaming.
type RuntimeEventType string

const (
	EventLLMToken         RuntimeEventType = "llm_token"
	EventLLMThinking      RuntimeEventType = "llm_thinking"
	EventLLMAnswer        RuntimeEventType = "llm_answer"
	EventToolStart        RuntimeEventType = "tool_start"
	EventToolStdout       RuntimeEventType = "tool_stdout"
	EventToolEnd          RuntimeEventType = "tool_end"
	EventExecutionSummary RuntimeEventType = "execution_summary"
	EventAgentError       RuntimeEventType = "agent_error"
)

// RuntimeEvent is the transport-neutral runtime streaming payload.
type RuntimeEvent struct {
	Type          RuntimeEventType `json:"type"`
	ChatID        string           `json:"chat_id,omitempty"`
	TraceID       string           `json:"trace_id,omitempty"`
	ExecutionMode string           `json:"execution_mode,omitempty"`
	Step          int              `json:"step,omitempty"`
	Reasoning     string           `json:"reasoning,omitempty"`
	ToolName      string           `json:"tool_name,omitempty"`
	ToolArguments map[string]any   `json:"tool_arguments,omitempty"`
	Data          string           `json:"data,omitempty"`
	Success       bool             `json:"success,omitempty"`
	Error         string           `json:"error,omitempty"`
	FinalStatus   string           `json:"final_status,omitempty"`
	TokenUsage    *TokenUsage      `json:"token_usage,omitempty"`
	At            time.Time        `json:"at"`
}

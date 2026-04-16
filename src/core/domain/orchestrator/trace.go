package orchestrator

import "time"

// TokenUsage captures per-turn LLM token usage.
type TokenUsage struct {
	PromptTokens        int `json:"prompt_tokens,omitempty"`
	CompletionTokens    int `json:"completion_tokens,omitempty"`
	TotalTokens         int `json:"total_tokens,omitempty"`
	CachedTokens        int `json:"cached_tokens,omitempty"`
	ThoughtsTokens      int `json:"thoughts_tokens,omitempty"`
	ToolUsePromptTokens int `json:"tool_use_prompt_tokens,omitempty"`
}

// Add aggregates another token usage sample into this one.
func (u *TokenUsage) Add(other TokenUsage) {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.TotalTokens += other.TotalTokens
	u.CachedTokens += other.CachedTokens
	u.ThoughtsTokens += other.ThoughtsTokens
	u.ToolUsePromptTokens += other.ToolUsePromptTokens
}

// IsZero reports whether all counters are zero.
func (u TokenUsage) IsZero() bool {
	return u.PromptTokens == 0 &&
		u.CompletionTokens == 0 &&
		u.TotalTokens == 0 &&
		u.CachedTokens == 0 &&
		u.ThoughtsTokens == 0 &&
		u.ToolUsePromptTokens == 0
}

// TraceStep captures one loop step with selected tool and resulting observation.
type TraceStep struct {
	Step          int            `json:"step"`
	Reasoning     string         `json:"reasoning,omitempty"`
	ToolName      string         `json:"tool_name,omitempty"`
	ToolArguments map[string]any `json:"tool_arguments,omitempty"`
	Observation   string         `json:"observation,omitempty"`
	ToolSuccess   bool           `json:"tool_success,omitempty"`
	ToolError     string         `json:"tool_error,omitempty"`
}

// ExecutionTrace is one user-turn execution trace emitted by orchestrator.
type ExecutionTrace struct {
	TraceID       string      `json:"trace_id"`
	ExecutionMode string      `json:"execution_mode"`
	Reasoning     string      `json:"reasoning,omitempty"`
	Steps         []TraceStep `json:"steps,omitempty"`
	FinalAnswer   string      `json:"final_answer,omitempty"`
	TokenUsage    TokenUsage  `json:"token_usage,omitempty"`
	StartedAt     time.Time   `json:"started_at"`
	CompletedAt   time.Time   `json:"completed_at,omitempty"`
}

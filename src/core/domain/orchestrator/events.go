package orchestrator

import "time"

// RuntimeEventType is emitted by the runtime orchestrator for client streaming.
type RuntimeEventType string

const (
	EventToken   RuntimeEventType = "token"
	EventThinking RuntimeEventType = "thinking"
	EventAnswer  RuntimeEventType = "answer"
	EventToolCall RuntimeEventType = "tool_call"
	EventError   RuntimeEventType = "error"
	EventDone    RuntimeEventType = "done"
	EventTitle   RuntimeEventType = "title"
	EventPlan    RuntimeEventType = "plan"
)

// RuntimeEvent is the transport-neutral runtime streaming payload.
type RuntimeEvent struct {
	Type       RuntimeEventType `json:"type"`
	ChatID     string           `json:"chat_id,omitempty"`
	TraceID    string           `json:"trace_id,omitempty"`
	Step       int              `json:"step,omitempty"`
	Data       string           `json:"data,omitempty"`
	Command    string           `json:"command,omitempty"`
	Result     string           `json:"result,omitempty"`
	Error      string           `json:"error,omitempty"`
	TokenUsage *TokenUsage      `json:"token_usage,omitempty"`
	Metadata   map[string]any   `json:"metadata,omitempty"`
	At         time.Time        `json:"at"`
}

// ToPersisted converts a streaming RuntimeEvent into a PersistedRuntimeEvent
// suitable for database insertion.
func (e RuntimeEvent) ToPersisted() *PersistedRuntimeEvent {
	return &PersistedRuntimeEvent{
		ChatID:     e.ChatID,
		TraceID:    e.TraceID,
		Type:       e.Type,
		Step:       e.Step,
		Data:       e.Data,
		Command:    e.Command,
		Result:     e.Result,
		Error:      e.Error,
		TokenUsage: e.TokenUsage,
		Metadata:   e.Metadata,
		CreatedAt:  e.At,
	}
}

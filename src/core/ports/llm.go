package ports

import (
	"context"

	"github.com/spacetrek-sh/spacetrek/src/core/domain/chat"
	orchdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/orchestrator"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
)

// PriorTurn represents one completed react-loop tool call and its result,
// used to give the planner multi-turn context within a single user turn.
type PriorTurn struct {
	ToolCall   ToolPlanStep
	ToolResult tool.Result
}

// PlanRequest is passed to planner implementations to choose tools.
type PlanRequest struct {
	ChatID     string
	AgentID    string
	UserID     string
	Message    string
	VMID       string
	History    []chat.Message
	PriorTurns []PriorTurn
}

// ToolPlanStep is one planned tool call.
type ToolPlanStep struct {
	Name             string
	Arguments        map[string]any
	ThoughtSignature []byte
}

// PlanMetadata carries optional planner metadata for one PlanTools call.
type PlanMetadata struct {
	Reasoning  string                `json:"reasoning,omitempty"`
	RawText    string                `json:"raw_text,omitempty"`
	Thinking   string                `json:"thinking,omitempty"`
	Answer     string                `json:"answer,omitempty"`
	TokenUsage orchdomain.TokenUsage `json:"token_usage,omitempty"`
}

// ToolPlan is the list of tool calls planned for one turn.
type ToolPlan struct {
	Steps []ToolPlanStep
}

// FinalResponseRequest is used to synthesize final assistant text.
type FinalResponseRequest struct {
	Message     string
	Plan        ToolPlan
	ToolResults []tool.Result
	History     []chat.Message
}

// FinalResponseMetadata carries optional metadata from final response generation.
type FinalResponseMetadata struct {
	Reasoning  string                `json:"reasoning,omitempty"`
	Thinking   string                `json:"thinking,omitempty"`
	TokenUsage orchdomain.TokenUsage `json:"token_usage,omitempty"`
}

// ToolPlanner abstracts LLM-driven planning and final synthesis.
type ToolPlanner interface {
	PlanTools(ctx context.Context, req PlanRequest) (ToolPlan, error)
	FinalResponse(ctx context.Context, req FinalResponseRequest) (string, error)
}

// ToolPlannerWithMetadata is an optional extension for planners that expose
// reasoning and token accounting metadata. Orchestrator callers should fall
// back to ToolPlanner methods when this interface is not implemented.
type ToolPlannerWithMetadata interface {
	PlanToolsWithMetadata(ctx context.Context, req PlanRequest) (ToolPlan, PlanMetadata, error)
	FinalResponseWithMetadata(ctx context.Context, req FinalResponseRequest) (string, FinalResponseMetadata, error)
}

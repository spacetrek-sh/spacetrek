package ports

import (
	"context"

	"github.com/kumori-sh/spacetrk/src/core/domain/chat"
	"github.com/kumori-sh/spacetrk/src/core/domain/tool"
)

// PlanRequest is passed to planner implementations to choose tools.
type PlanRequest struct {
	ChatID   string
	AgentID  string
	UserID   string
	Message  string
	VMID     string
	History  []chat.Message
}

// ToolPlanStep is one planned tool call.
type ToolPlanStep struct {
	Name      string
	Arguments map[string]any
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

// ToolPlanner abstracts LLM-driven planning and final synthesis.
type ToolPlanner interface {
	PlanTools(ctx context.Context, req PlanRequest) (ToolPlan, error)
	FinalResponse(ctx context.Context, req FinalResponseRequest) (string, error)
}

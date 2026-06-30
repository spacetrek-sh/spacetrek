package toolsvc

import (
	"context"
	"fmt"
	"strings"

	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
)

const (
	planMaxSteps = 10
	planMinSteps = 1
)

// PlanAnnounceTool lets the LLM publish a multi-step plan that the user can
// see (via the plan SSE event) before execution continues. Non-blocking: the
// orchestrator does NOT wait for user approval after this tool returns.
type PlanAnnounceTool struct{}

func NewPlanAnnounceTool() *PlanAnnounceTool {
	return &PlanAnnounceTool{}
}

func (t *PlanAnnounceTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "plan.announce",
		Description: "Announce a multi-step plan the user can see before you start executing it. Use this when the task needs 2+ sequential steps so the user can redirect early. Non-blocking — the orchestrator continues executing after announcing; the user interrupts via the chat input if they want to change course.",
		Parameters: map[string]tool.Parameter{
			"summary": {
				Type:        "string",
				Required:    true,
				Description: "1-2 sentence summary of what you are about to do and why.",
			},
			"steps": {
				Type:        "array",
				Required:    true,
				Description: "Ordered list of 1-10 steps. Each item is an object {\"description\": string}.",
				Items: &tool.Parameter{
					Type: "object",
					Properties: map[string]tool.Parameter{
						"description": {
							Type:        "string",
							Description: "What this step does, in one sentence.",
						},
					},
				},
			},
		},
	}
}

func (t *PlanAnnounceTool) Execute(_ context.Context, call tool.Call) (tool.Result, error) {
	result := tool.Result{ToolCallID: call.ID, ToolName: call.Name}

	summary, ok := readStringArg(call.Arguments, "summary")
	if !ok {
		result.OK = false
		result.Error = "missing required argument summary"
		return result, nil
	}

	rawSteps, ok := call.Arguments["steps"].([]any)
	if !ok {
		result.OK = false
		result.Error = "missing or invalid required argument steps (expected array)"
		return result, nil
	}
	if len(rawSteps) < planMinSteps || len(rawSteps) > planMaxSteps {
		result.OK = false
		result.Error = fmt.Sprintf("steps must contain %d-%d items, got %d", planMinSteps, planMaxSteps, len(rawSteps))
		return result, nil
	}

	steps := make([]map[string]string, 0, len(rawSteps))
	for i, s := range rawSteps {
		m, ok := s.(map[string]any)
		if !ok {
			result.OK = false
			result.Error = fmt.Sprintf("steps[%d]: expected object with a description field", i)
			return result, nil
		}
		desc, ok := m["description"].(string)
		if !ok || strings.TrimSpace(desc) == "" {
			result.OK = false
			result.Error = fmt.Sprintf("steps[%d]: missing or empty 'description' string", i)
			return result, nil
		}
		steps = append(steps, map[string]string{"description": desc})
	}

	result.OK = true
	result.Payload = map[string]any{
		"summary":    summary,
		"steps":      steps,
		"announced":  true,
		"step_count": len(steps),
	}
	return result, nil
}

var _ tool.Tool = (*PlanAnnounceTool)(nil)

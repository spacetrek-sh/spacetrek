package orchestratorsvc

import (
	"context"
	"fmt"
	"strings"

	"github.com/spacetrek-sh/spacetrek/src/core/ports"
)

// RulePlanner is a deterministic planner used as bootstrap before real LLM integration.
type RulePlanner struct{}

func NewRulePlanner() *RulePlanner {
	return &RulePlanner{}
}

func (p *RulePlanner) PlanTools(ctx context.Context, req ports.PlanRequest) (ports.ToolPlan, error) {
	plan, _, err := p.PlanToolsWithMetadata(ctx, req)
	return plan, err
}

func (p *RulePlanner) PlanToolsWithMetadata(_ context.Context, req ports.PlanRequest) (ports.ToolPlan, ports.PlanMetadata, error) {
	trimmed := strings.TrimSpace(req.Message)
	if strings.HasPrefix(trimmed, "/exec ") {
		command := strings.TrimSpace(strings.TrimPrefix(trimmed, "/exec "))
		if command == "" || strings.TrimSpace(req.VMID) == "" {
			return ports.ToolPlan{}, ports.PlanMetadata{}, nil
		}
		return ports.ToolPlan{
			Steps: []ports.ToolPlanStep{
				{
					Name: "vm.execute_command",
					Arguments: map[string]any{
						"vm_id":   req.VMID,
						"command": command,
					},
				},
			},
		}, ports.PlanMetadata{Reasoning: "Matched /exec rule and generated vm.execute_command call."}, nil
	}

	return ports.ToolPlan{}, ports.PlanMetadata{}, nil
}

func (p *RulePlanner) FinalResponse(ctx context.Context, req ports.FinalResponseRequest) (string, error) {
	text, _, err := p.FinalResponseWithMetadata(ctx, req)
	return text, err
}

func (p *RulePlanner) FinalResponseWithMetadata(_ context.Context, req ports.FinalResponseRequest) (string, ports.FinalResponseMetadata, error) {
	trimmed := strings.TrimSpace(req.Message)
	if len(req.ToolResults) == 0 {
		if strings.HasPrefix(trimmed, "/exec ") {
			return "No command was executed. Provide vm_id and a non-empty /exec command.", ports.FinalResponseMetadata{}, nil
		}
		return "I received your message. No tool call was required for this turn.", ports.FinalResponseMetadata{}, nil
	}

	lines := make([]string, 0, len(req.ToolResults)+1)
	lines = append(lines, "Tool execution summary:")
	for _, result := range req.ToolResults {
		if result.OK {
			lines = append(lines, fmt.Sprintf("- %s: success", result.ToolName))
			payload := strings.TrimSpace(payloadToString(result.Payload))
			if payload != "" {
				lines = append(lines, payload)
			}
			continue
		}

		if result.Error == "" {
			lines = append(lines, fmt.Sprintf("- %s: failed", result.ToolName))
		} else {
			lines = append(lines, fmt.Sprintf("- %s: failed (%s)", result.ToolName, result.Error))
		}
	}

	return strings.Join(lines, "\n"), ports.FinalResponseMetadata{}, nil
}

func payloadToString(payload any) string {
	switch p := payload.(type) {
	case string:
		return p
	case map[string]any:
		if out, ok := p["output"].(string); ok {
			return out
		}
	}
	return ""
}

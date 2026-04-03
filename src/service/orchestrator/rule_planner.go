package orchestratorsvc

import (
	"context"
	"fmt"
	"strings"

	"github.com/kumori-sh/spacetrk/src/core/ports"
)

// RulePlanner is a deterministic planner used as bootstrap before real LLM integration.
type RulePlanner struct{}

func NewRulePlanner() *RulePlanner {
	return &RulePlanner{}
}

func (p *RulePlanner) PlanTools(_ context.Context, req ports.PlanRequest) (ports.ToolPlan, error) {
	trimmed := strings.TrimSpace(req.Message)
	if strings.HasPrefix(trimmed, "/exec ") {
		command := strings.TrimSpace(strings.TrimPrefix(trimmed, "/exec "))
		if command == "" || strings.TrimSpace(req.VMID) == "" {
			return ports.ToolPlan{}, nil
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
		}, nil
	}

	return ports.ToolPlan{}, nil
}

func (p *RulePlanner) FinalResponse(_ context.Context, req ports.FinalResponseRequest) (string, error) {
	trimmed := strings.TrimSpace(req.Message)
	if len(req.ToolResults) == 0 {
		if strings.HasPrefix(trimmed, "/exec ") {
			return "No command was executed. Provide vm_id and a non-empty /exec command.", nil
		}
		return "I received your message. No tool call was required for this turn.", nil
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

	return strings.Join(lines, "\n"), nil
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

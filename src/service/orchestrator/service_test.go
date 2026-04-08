package orchestratorsvc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kumori-sh/spacetrk/src/core/domain/chat"
	"github.com/kumori-sh/spacetrk/src/core/domain/tool"
	"github.com/kumori-sh/spacetrk/src/core/ports"
)

type fakePlanner struct {
	planCalls int
}

func (p *fakePlanner) PlanTools(_ context.Context, req ports.PlanRequest) (ports.ToolPlan, error) {
	p.planCalls++
	switch p.planCalls {
	case 1:
		return ports.ToolPlan{Steps: []ports.ToolPlanStep{{Name: "vm.execute_command", Arguments: map[string]any{"vm_id": req.VMID, "command": "echo step1"}}}}, nil
	case 2:
		return ports.ToolPlan{Steps: []ports.ToolPlanStep{{Name: "vm.execute_command", Arguments: map[string]any{"vm_id": req.VMID, "command": "echo step2"}}}}, nil
	default:
		return ports.ToolPlan{}, nil
	}
}

func (p *fakePlanner) FinalResponse(_ context.Context, req ports.FinalResponseRequest) (string, error) {
	return fmt.Sprintf("done with %d steps", len(req.ToolResults)), nil
}

type fakeToolRegistry struct {
	tool tool.Tool
}

func (r *fakeToolRegistry) Get(name string) (tool.Tool, bool) {
	if r.tool == nil || name != r.tool.Definition().Name {
		return nil, false
	}
	return r.tool, true
}

func (r *fakeToolRegistry) List() []tool.Definition {
	if r.tool == nil {
		return nil
	}
	return []tool.Definition{r.tool.Definition()}
}

type fakeTool struct {
	execCount int
}

func (t *fakeTool) Definition() tool.Definition {
	return tool.Definition{Name: "vm.execute_command"}
}

func (t *fakeTool) Execute(_ context.Context, call tool.Call) (tool.Result, error) {
	t.execCount++
	return tool.Result{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		OK:         true,
		Payload:    map[string]any{"output": fmt.Sprintf("observation-%d", t.execCount)},
	}, nil
}

func TestProcess_ReactLoopExecutesIterativeSteps(t *testing.T) {
	planner := &fakePlanner{}
	execTool := &fakeTool{}
	reg := &fakeToolRegistry{tool: execTool}
	store := NewMemoryStateStore()

	svc := NewWithConfig(planner, reg, store, Config{
		AllowedTools:  map[string]struct{}{"vm.execute_command": {}},
		ToolTimeout:   2 * time.Second,
		MaxReactSteps: 4,
	})

	result, err := svc.Process(context.Background(), ProcessInput{
		ChatID:  "s-1",
		AgentID: "a-1",
		UserID:  "u-1",
		Message: "hello",
		VMID:    "vm-1",
		History: []chat.Message{},
	})
	if err != nil {
		t.Fatalf("process react loop: %v", err)
	}

	if len(result.ToolResults) != 2 {
		t.Fatalf("expected 2 tool results, got %d", len(result.ToolResults))
	}
	if execTool.execCount != 2 {
		t.Fatalf("expected tool execute count 2, got %d", execTool.execCount)
	}
	if result.AssistantMessage != "done with 2 steps" {
		t.Fatalf("unexpected assistant message: %q", result.AssistantMessage)
	}
}

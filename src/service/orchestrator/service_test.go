package orchestratorsvc

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spacetrek-sh/spacetrek/src/core/domain/chat"
	orchdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/orchestrator"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
	"github.com/spacetrek-sh/spacetrek/src/core/ports"
	toolsvc "github.com/spacetrek-sh/spacetrek/src/service/tool"
)

type fakePlanner struct {
	planCalls int
}

func (p *fakePlanner) PlanTools(ctx context.Context, req ports.PlanRequest) (ports.ToolPlan, error) {
	plan, _, err := p.PlanToolsWithMetadata(ctx, req)
	return plan, err
}

func (p *fakePlanner) PlanToolsWithMetadata(_ context.Context, req ports.PlanRequest) (ports.ToolPlan, ports.PlanMetadata, error) {
	vmID := ""
	if len(req.AvailableVMs) > 0 {
		vmID = req.AvailableVMs[0].VMID
	}
	p.planCalls++
	switch p.planCalls {
	case 1:
		return ports.ToolPlan{Steps: []ports.ToolPlanStep{{Name: "vm.execute_command", Arguments: map[string]any{"vm_id": vmID, "command": "echo step1"}}}}, ports.PlanMetadata{
			Reasoning: "Need first execution step.",
			TokenUsage: orchdomain.TokenUsage{
				PromptTokens:     10,
				CompletionTokens: 3,
				TotalTokens:      13,
			},
		}, nil
	case 2:
		return ports.ToolPlan{Steps: []ports.ToolPlanStep{{Name: "vm.execute_command", Arguments: map[string]any{"vm_id": vmID, "command": "echo step2"}}}}, ports.PlanMetadata{
			Reasoning: "Need second execution step.",
			TokenUsage: orchdomain.TokenUsage{
				PromptTokens:     8,
				CompletionTokens: 2,
				TotalTokens:      10,
			},
		}, nil
	default:
		return ports.ToolPlan{}, ports.PlanMetadata{}, nil
	}
}

func (p *fakePlanner) FinalResponse(ctx context.Context, req ports.FinalResponseRequest) (string, error) {
	text, _, err := p.FinalResponseWithMetadata(ctx, req)
	return text, err
}

func (p *fakePlanner) FinalResponseWithMetadata(_ context.Context, req ports.FinalResponseRequest) (string, ports.FinalResponseMetadata, error) {
	return fmt.Sprintf("done with %d steps", len(req.ToolResults)), ports.FinalResponseMetadata{
		Reasoning: "All tool outputs collected; synthesizing final answer.",
		TokenUsage: orchdomain.TokenUsage{
			PromptTokens:     12,
			CompletionTokens: 6,
			TotalTokens:      18,
		},
	}, nil
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
		AvailableVMs: []ports.AvailableVM{
			{VMID: "vm-1", Environment: "ubuntu", Status: "running"},
		},
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
	if result.Trace == nil {
		t.Fatal("expected non-nil trace")
	}
	if result.Trace.TraceID == "" {
		t.Fatal("expected trace id to be set")
	}
	if got := len(result.Trace.Steps); got != 2 {
		t.Fatalf("expected 2 trace steps, got %d", got)
	}
	if result.Trace.Steps[0].Reasoning == "" {
		t.Fatal("expected step reasoning to be captured")
	}
	if result.Trace.FinalAnswer != "done with 2 steps" {
		t.Fatalf("unexpected trace final answer: %q", result.Trace.FinalAnswer)
	}
	if result.Trace.TokenUsage.TotalTokens != 41 {
		t.Fatalf("expected total tokens 41, got %d", result.Trace.TokenUsage.TotalTokens)
	}
}

func TestProcess_StructuredPayloadObservation(t *testing.T) {
	planner := &fakeVMListPlanner{}
	vmListTool := &fakeVMListTool{}
	reg := &fakeToolRegistry{tool: vmListTool}
	store := NewMemoryStateStore()

	svc := NewWithConfig(planner, reg, store, Config{
		AllowedTools:  map[string]struct{}{"vm.list": {}},
		ToolTimeout:   2 * time.Second,
		MaxReactSteps: 4,
	})

	result, err := svc.Process(context.Background(), ProcessInput{
		ChatID:  "s-2",
		AgentID: "a-1",
		UserID:  "u-1",
		Message: "list my VMs",
		History: []chat.Message{},
	})
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	if len(result.ToolResults) != 1 {
		t.Fatalf("expected 1 tool result, got %d", len(result.ToolResults))
	}
	if len(result.Trace.Steps) != 1 {
		t.Fatalf("expected 1 trace step, got %d", len(result.Trace.Steps))
	}
	obs := result.Trace.Steps[0].Observation
	if obs == "" || obs == "(no observation)" {
		t.Fatalf("expected JSON observation, got %q", obs)
	}
	if !strings.Contains(obs, "vm-abc") {
		t.Fatalf("expected observation to contain vm_id 'vm-abc', got %q", obs)
	}
}

// fakeVMListPlanner returns vm.list once, then text-only.
type fakeVMListPlanner struct {
	calls int
}

func (p *fakeVMListPlanner) PlanTools(ctx context.Context, req ports.PlanRequest) (ports.ToolPlan, error) {
	plan, _, err := p.PlanToolsWithMetadata(ctx, req)
	return plan, err
}

func (p *fakeVMListPlanner) PlanToolsWithMetadata(_ context.Context, _ ports.PlanRequest) (ports.ToolPlan, ports.PlanMetadata, error) {
	p.calls++
	if p.calls == 1 {
		return ports.ToolPlan{Steps: []ports.ToolPlanStep{{Name: "vm.list", Arguments: map[string]any{}}}}, ports.PlanMetadata{
			Reasoning: "Need to list VMs.",
			TokenUsage: orchdomain.TokenUsage{
				PromptTokens:     5,
				CompletionTokens: 2,
				TotalTokens:      7,
			},
		}, nil
	}
	return ports.ToolPlan{}, ports.PlanMetadata{}, nil
}

func (p *fakeVMListPlanner) FinalResponse(ctx context.Context, req ports.FinalResponseRequest) (string, error) {
	text, _, err := p.FinalResponseWithMetadata(ctx, req)
	return text, err
}

func (p *fakeVMListPlanner) FinalResponseWithMetadata(_ context.Context, req ports.FinalResponseRequest) (string, ports.FinalResponseMetadata, error) {
	return "listed VMs", ports.FinalResponseMetadata{
		TokenUsage: orchdomain.TokenUsage{
			PromptTokens:     3,
			CompletionTokens: 1,
			TotalTokens:      4,
		},
	}, nil
}

// fakeVMListTool returns a structured payload without "output" key.
type fakeVMListTool struct{}

func (t *fakeVMListTool) Definition() tool.Definition {
	return tool.Definition{Name: "vm.list"}
}

func (t *fakeVMListTool) Execute(_ context.Context, call tool.Call) (tool.Result, error) {
	return tool.Result{
		ToolCallID: call.ID,
		ToolName:   call.Name,
		OK:         true,
		Payload: map[string]any{
			"vms": []any{
				map[string]any{"vm_id": "vm-abc", "status": "running", "provider": "firecracker"},
			},
		},
	}, nil
}

// TestProcess_PlanAnnounceEmitsPlanEvent asserts the orchestrator fires a
// structured plan SSE event when the LLM calls plan.announce. The plan event
// must precede the generic tool_call event for the same step so frontends can
// render the checklist before reporting the tool result.
func TestProcess_PlanAnnounceEmitsPlanEvent(t *testing.T) {
	planner := &fakePlanAnnouncePlanner{}
	reg := &fakeToolRegistry{tool: toolsvc.NewPlanAnnounceTool()}
	store := NewMemoryStateStore()

	var emitted []orchdomain.RuntimeEvent
	emit := func(e orchdomain.RuntimeEvent) { emitted = append(emitted, e) }

	svc := NewWithConfig(planner, reg, store, Config{
		AllowedTools:  map[string]struct{}{"plan.announce": {}},
		ToolTimeout:   2 * time.Second,
		MaxReactSteps: 4,
	})

	if _, err := svc.Process(context.Background(), ProcessInput{
		ChatID:  "s-plan",
		AgentID: "a-1",
		UserID:  "u-1",
		Message: "build it",
		History: []chat.Message{},
		EmitEvent: emit,
	}); err != nil {
		t.Fatalf("process: %v", err)
	}

	var planEvents []orchdomain.RuntimeEvent
	var toolCalls []orchdomain.RuntimeEvent
	for _, e := range emitted {
		switch e.Type {
		case orchdomain.EventPlan:
			planEvents = append(planEvents, e)
		case orchdomain.EventToolCall:
			toolCalls = append(toolCalls, e)
		}
	}

	if len(planEvents) != 1 {
		t.Fatalf("expected 1 plan event, got %d", len(planEvents))
	}
	pe := planEvents[0]
	if pe.Data != "summary-text" {
		t.Fatalf("plan Data = %q, want %q", pe.Data, "summary-text")
	}
	if pe.Metadata["summary"] != "summary-text" {
		t.Fatalf("plan metadata.summary = %v", pe.Metadata["summary"])
	}
	steps, ok := pe.Metadata["steps"].([]map[string]string)
	if !ok {
		t.Fatalf("plan metadata.steps type: %T", pe.Metadata["steps"])
	}
	if len(steps) != 2 || steps[0]["description"] != "first" {
		t.Fatalf("plan steps mismatch: %+v", steps)
	}

	if len(toolCalls) == 0 {
		t.Fatal("expected a tool_call event after plan event")
	}
}

// fakePlanAnnouncePlanner returns plan.announce once, then text-only.
type fakePlanAnnouncePlanner struct {
	calls int
}

func (p *fakePlanAnnouncePlanner) PlanTools(ctx context.Context, req ports.PlanRequest) (ports.ToolPlan, error) {
	plan, _, err := p.PlanToolsWithMetadata(ctx, req)
	return plan, err
}

func (p *fakePlanAnnouncePlanner) PlanToolsWithMetadata(_ context.Context, _ ports.PlanRequest) (ports.ToolPlan, ports.PlanMetadata, error) {
	p.calls++
	if p.calls == 1 {
		return ports.ToolPlan{Steps: []ports.ToolPlanStep{{Name: "plan.announce", Arguments: map[string]any{
			"summary": "summary-text",
			"steps": []any{
				map[string]any{"description": "first"},
				map[string]any{"description": "second"},
			},
		}}}}, ports.PlanMetadata{}, nil
	}
	return ports.ToolPlan{}, ports.PlanMetadata{}, nil
}

func (p *fakePlanAnnouncePlanner) FinalResponse(ctx context.Context, req ports.FinalResponseRequest) (string, error) {
	text, _, err := p.FinalResponseWithMetadata(ctx, req)
	return text, err
}

func (p *fakePlanAnnouncePlanner) FinalResponseWithMetadata(_ context.Context, _ ports.FinalResponseRequest) (string, ports.FinalResponseMetadata, error) {
	return "announced", ports.FinalResponseMetadata{}, nil
}

package toolsvc

import (
	"context"
	"strings"
	"testing"

	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
)

func TestPlanAnnounceTool_ValidInput(t *testing.T) {
	tl := NewPlanAnnounceTool()
	res, err := tl.Execute(context.Background(), tool.Call{
		ID:   "c1",
		Name: "plan.announce",
		Arguments: map[string]any{
			"summary": "Deploy the API",
			"steps": []any{
				map[string]any{"description": "create VM"},
				map[string]any{"description": "write server.js"},
				map[string]any{"description": "start server"},
			},
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected OK, got error: %s", res.Error)
	}
	payload, ok := res.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type: %T", res.Payload)
	}
	if payload["summary"] != "Deploy the API" {
		t.Fatalf("summary = %v", payload["summary"])
	}
	if payload["announced"] != true {
		t.Fatalf("announced flag missing")
	}
	if payload["step_count"] != 3 {
		t.Fatalf("step_count = %v, want 3", payload["step_count"])
	}
	steps, ok := payload["steps"].([]map[string]string)
	if !ok {
		t.Fatalf("steps type: %T", payload["steps"])
	}
	if len(steps) != 3 || steps[0]["description"] != "create VM" {
		t.Fatalf("steps mismatch: %+v", steps)
	}
}

func TestPlanAnnounceTool_MissingSummary(t *testing.T) {
	tl := NewPlanAnnounceTool()
	res, _ := tl.Execute(context.Background(), tool.Call{
		ID:   "c1",
		Name: "plan.announce",
		Arguments: map[string]any{
			"steps": []any{map[string]any{"description": "x"}},
		},
	})
	if res.OK {
		t.Fatal("expected failure on missing summary")
	}
}

func TestPlanAnnounceTool_MissingSteps(t *testing.T) {
	tl := NewPlanAnnounceTool()
	res, _ := tl.Execute(context.Background(), tool.Call{
		ID:   "c1",
		Name: "plan.announce",
		Arguments: map[string]any{
			"summary": "do thing",
		},
	})
	if res.OK {
		t.Fatal("expected failure on missing steps")
	}
}

func TestPlanAnnounceTool_StepsMustBeArray(t *testing.T) {
	tl := NewPlanAnnounceTool()
	res, _ := tl.Execute(context.Background(), tool.Call{
		ID:   "c1",
		Name: "plan.announce",
		Arguments: map[string]any{
			"summary": "do thing",
			"steps":   "not-an-array",
		},
	})
	if res.OK {
		t.Fatal("expected failure when steps is not an array")
	}
}

func TestPlanAnnounceTool_RejectsEmptySteps(t *testing.T) {
	tl := NewPlanAnnounceTool()
	res, _ := tl.Execute(context.Background(), tool.Call{
		ID:   "c1",
		Name: "plan.announce",
		Arguments: map[string]any{
			"summary": "do thing",
			"steps":   []any{},
		},
	})
	if res.OK {
		t.Fatal("expected failure on empty steps")
	}
}

func TestPlanAnnounceTool_RejectsTooManySteps(t *testing.T) {
	tl := NewPlanAnnounceTool()
	tooMany := make([]any, planMaxSteps+1)
	for i := range tooMany {
		tooMany[i] = map[string]any{"description": "s"}
	}
	res, _ := tl.Execute(context.Background(), tool.Call{
		ID:   "c1",
		Name: "plan.announce",
		Arguments: map[string]any{
			"summary": "do thing",
			"steps":   tooMany,
		},
	})
	if res.OK {
		t.Fatalf("expected failure with %d steps (max %d)", planMaxSteps+1, planMaxSteps)
	}
}

func TestPlanAnnounceTool_RejectsStepWithoutDescription(t *testing.T) {
	tl := NewPlanAnnounceTool()
	res, _ := tl.Execute(context.Background(), tool.Call{
		ID:   "c1",
		Name: "plan.announce",
		Arguments: map[string]any{
			"summary": "do thing",
			"steps": []any{
				map[string]any{"description": "ok"},
				map[string]any{"note": "missing desc"},
			},
		},
	})
	if res.OK {
		t.Fatal("expected failure when step[1] lacks description")
	}
	if !strings.Contains(res.Error, "steps[1]") {
		t.Fatalf("error should reference steps[1], got: %s", res.Error)
	}
}

func TestPlanAnnounceTool_RejectsEmptyDescription(t *testing.T) {
	tl := NewPlanAnnounceTool()
	res, _ := tl.Execute(context.Background(), tool.Call{
		ID:   "c1",
		Name: "plan.announce",
		Arguments: map[string]any{
			"summary": "do thing",
			"steps": []any{
				map[string]any{"description": "   "},
			},
		},
	})
	if res.OK {
		t.Fatal("expected failure on whitespace-only description")
	}
}

func TestPlanAnnounceTool_AcceptsMaxSteps(t *testing.T) {
	tl := NewPlanAnnounceTool()
	max := make([]any, planMaxSteps)
	for i := range max {
		max[i] = map[string]any{"description": "s"}
	}
	res, _ := tl.Execute(context.Background(), tool.Call{
		ID:   "c1",
		Name: "plan.announce",
		Arguments: map[string]any{
			"summary": "do thing",
			"steps":   max,
		},
	})
	if !res.OK {
		t.Fatalf("expected OK at max step count, got: %s", res.Error)
	}
}

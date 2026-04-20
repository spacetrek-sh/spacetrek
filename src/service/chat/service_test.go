package chatsvc

import (
	"context"
	"testing"
	"time"

	"github.com/spacetrek-sh/spacetrek/src/core/domain/chat"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/orchestrator"
	"github.com/spacetrek-sh/spacetrek/src/repository/memory"
	orchestratorsvc "github.com/spacetrek-sh/spacetrek/src/service/orchestrator"
)

type fakeOrchestrator struct {
	lastInput orchestratorsvc.ProcessInput
}

func (f *fakeOrchestrator) Process(_ context.Context, input orchestratorsvc.ProcessInput) (orchestratorsvc.ProcessResult, error) {
	f.lastInput = input
	return orchestratorsvc.ProcessResult{
		AssistantMessage: "ok",
		Trace: &orchestrator.ExecutionTrace{
			TraceID:       "trace-1",
			ExecutionMode: "react_loop",
			Reasoning:     "Tool execution required for request.",
			Steps: []orchestrator.TraceStep{
				{Step: 1, ToolName: "vm.execute_command", Observation: "hello", ToolSuccess: true},
			},
			FinalAnswer: "ok",
			TokenUsage: orchestrator.TokenUsage{
				PromptTokens:     7,
				CompletionTokens: 2,
				TotalTokens:      9,
			},
			StartedAt:   time.Now().UTC(),
			CompletedAt: time.Now().UTC(),
		},
	}, nil
}

func TestSendMessage_ForwardsToOrchestrator(t *testing.T) {
	agentRepo := memory.NewAgentRepository()
	chatRepo := memory.NewChatRepository()
	orch := &fakeOrchestrator{}
	svc := New(chatRepo, memory.NewRuntimeEventRepository(), agentRepo, orch, nil)

	created, err := svc.Create(context.Background(), chat.CreateParams{})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}

	_, err = svc.SendMessage(context.Background(), created.ID, "hello", "vm-1")
	if err != nil {
		t.Fatalf("send message: %v", err)
	}

	if orch.lastInput.ChatID != created.ID {
		t.Fatalf("expected chat id %q, got %q", created.ID, orch.lastInput.ChatID)
	}

	if orch.lastInput.Message != "hello" {
		t.Fatalf("expected message %q, got %q", "hello", orch.lastInput.Message)
	}

	if orch.lastInput.VMID != "vm-1" {
		t.Fatalf("expected vm_id %q, got %q", "vm-1", orch.lastInput.VMID)
	}

	updated, err := svc.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("get chat: %v", err)
	}
	if len(updated.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(updated.Messages))
	}
	assistant := updated.Messages[1]
	execMeta, ok := assistant.Metadata["execution"].(map[string]any)
	if !ok {
		t.Fatalf("expected execution metadata map, got %#v", assistant.Metadata)
	}
	if traceID, _ := execMeta["trace_id"].(string); traceID != "trace-1" {
		t.Fatalf("expected trace_id trace-1, got %q", traceID)
	}
}

func TestSendOrCreate_AutoCreatesWhenNoID(t *testing.T) {
	agentRepo := memory.NewAgentRepository()
	chatRepo := memory.NewChatRepository()
	orch := &fakeOrchestrator{}
	svc := New(chatRepo, memory.NewRuntimeEventRepository(), agentRepo, orch, nil)

	c, err := svc.SendOrCreate(context.Background(), "", "hello", chat.CreateParams{})
	if err != nil {
		t.Fatalf("send or create: %v", err)
	}

	if c.ID == "" {
		t.Fatal("expected non-empty chat id")
	}
	if len(c.Messages) != 2 {
		t.Fatalf("expected 2 messages (user + assistant), got %d", len(c.Messages))
	}
	if orch.lastInput.ChatID != c.ID {
		t.Fatalf("expected chat id %q, got %q", c.ID, orch.lastInput.ChatID)
	}
}

func TestSendOrCreate_UsesExistingWhenIDProvided(t *testing.T) {
	agentRepo := memory.NewAgentRepository()
	chatRepo := memory.NewChatRepository()
	orch := &fakeOrchestrator{}
	svc := New(chatRepo, memory.NewRuntimeEventRepository(), agentRepo, orch, nil)

	created, err := svc.Create(context.Background(), chat.CreateParams{})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}

	c, err := svc.SendOrCreate(context.Background(), created.ID, "follow-up", chat.CreateParams{})
	if err != nil {
		t.Fatalf("send or create: %v", err)
	}

	if c.ID != created.ID {
		t.Fatalf("expected same chat id %q, got %q", created.ID, c.ID)
	}
}

func TestSubscribeRuntimeEvents_ReceivesPublishedEvents(t *testing.T) {
	agentRepo := memory.NewAgentRepository()
	chatRepo := memory.NewChatRepository()
	orch := &fakeOrchestrator{}
	svc := New(chatRepo, memory.NewRuntimeEventRepository(), agentRepo, orch, nil)

	created, err := svc.Create(context.Background(), chat.CreateParams{})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := svc.SubscribeRuntimeEvents(ctx, created.ID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	svc.publish(orchestrator.RuntimeEvent{Type: orchestrator.EventToken, ChatID: created.ID, Data: "x", At: time.Now().UTC()})

	select {
	case ev := <-events:
		if ev.Type != orchestrator.EventToken {
			t.Fatalf("expected %q, got %q", orchestrator.EventToken, ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runtime event")
	}
}

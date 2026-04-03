package sessionsvc

import (
	"context"
	"testing"
	"time"

	"github.com/kumori-sh/spacetrk/src/core/domain/orchestrator"
	"github.com/kumori-sh/spacetrk/src/core/domain/session"
	"github.com/kumori-sh/spacetrk/src/repository/memory"
	orchestratorsvc "github.com/kumori-sh/spacetrk/src/service/orchestrator"
)

type fakeOrchestrator struct {
	lastInput orchestratorsvc.ProcessInput
}

func (f *fakeOrchestrator) Process(_ context.Context, input orchestratorsvc.ProcessInput) (orchestratorsvc.ProcessResult, error) {
	f.lastInput = input
	return orchestratorsvc.ProcessResult{
		AssistantMessage: "ok",
	}, nil
}

func TestSendMessage_ForwardsToOrchestrator(t *testing.T) {
	agentRepo := memory.NewAgentRepository()
	sessionRepo := memory.NewSessionRepository()
	orch := &fakeOrchestrator{}
	svc := New(sessionRepo, agentRepo, orch)

	created, err := svc.Create(context.Background(), session.CreateParams{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	_, err = svc.SendMessage(context.Background(), created.ID, "hello", "vm-1")
	if err != nil {
		t.Fatalf("send message: %v", err)
	}

	if orch.lastInput.SessionID != created.ID {
		t.Fatalf("expected session id %q, got %q", created.ID, orch.lastInput.SessionID)
	}

	if orch.lastInput.Message != "hello" {
		t.Fatalf("expected message %q, got %q", "hello", orch.lastInput.Message)
	}

	if orch.lastInput.VMID != "vm-1" {
		t.Fatalf("expected vm_id %q, got %q", "vm-1", orch.lastInput.VMID)
	}
}

func TestSubscribeRuntimeEvents_ReceivesPublishedEvents(t *testing.T) {
	agentRepo := memory.NewAgentRepository()
	sessionRepo := memory.NewSessionRepository()
	orch := &fakeOrchestrator{}
	svc := New(sessionRepo, agentRepo, orch)

	created, err := svc.Create(context.Background(), session.CreateParams{})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := svc.SubscribeRuntimeEvents(ctx, created.ID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	svc.publish(orchestrator.RuntimeEvent{Type: orchestrator.EventLLMToken, SessionID: created.ID, Data: "x", At: time.Now().UTC()})

	select {
	case ev := <-events:
		if ev.Type != orchestrator.EventLLMToken {
			t.Fatalf("expected %q, got %q", orchestrator.EventLLMToken, ev.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runtime event")
	}
}

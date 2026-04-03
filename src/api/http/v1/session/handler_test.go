package sessionhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	orchdomain "github.com/kumori-sh/spacetrk/src/core/domain/orchestrator"
	"github.com/kumori-sh/spacetrk/src/core/domain/session"
)

type stubSessionService struct {
	lastVMID string
}

func (s *stubSessionService) Create(context.Context, session.CreateParams) (*session.Session, error) {
	now := time.Now().UTC()
	return &session.Session{ID: "s1", AgentID: "a1", UserID: "u1", Status: session.StatusActive, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *stubSessionService) Get(context.Context, string) (*session.Session, error) {
	now := time.Now().UTC()
	return &session.Session{ID: "s1", AgentID: "a1", UserID: "u1", Status: session.StatusActive, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *stubSessionService) SendMessage(_ context.Context, id, content, vmID string) (*session.Session, error) {
	s.lastVMID = vmID
	now := time.Now().UTC()
	return &session.Session{ID: id, AgentID: "a1", UserID: "u1", Status: session.StatusActive, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *stubSessionService) SubscribeRuntimeEvents(context.Context, string) (<-chan orchdomain.RuntimeEvent, error) {
	ch := make(chan orchdomain.RuntimeEvent)
	return ch, nil
}

func (s *stubSessionService) Close(context.Context, string) error { return nil }

func TestSendMessage_ForwardsVMID(t *testing.T) {
	svc := &stubSessionService{}
	h := NewHandler(svc)

	r := chi.NewRouter()
	h.RegisterRoutes(r)

	body, _ := json.Marshal(map[string]any{
		"content": "hello",
		"vm_id":   "vm-42",
	})

	req := httptest.NewRequest(http.MethodPost, "/sessions/sess-1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if svc.lastVMID != "vm-42" {
		t.Fatalf("expected vm_id %q, got %q", "vm-42", svc.lastVMID)
	}
}

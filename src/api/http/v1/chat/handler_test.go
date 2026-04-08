package chathttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kumori-sh/spacetrk/pkg/auth/jwt"
	orchdomain "github.com/kumori-sh/spacetrk/src/core/domain/orchestrator"
	"github.com/kumori-sh/spacetrk/src/core/domain/chat"
)

type stubChatService struct {
	lastID      string
	lastContent string
	lastParams  chat.CreateParams
}

func (s *stubChatService) SendOrCreate(_ context.Context, id, content string, p chat.CreateParams) (*chat.Chat, error) {
	s.lastID = id
	s.lastContent = content
	s.lastParams = p
	now := time.Now().UTC()
	return &chat.Chat{ID: "c1", AgentID: "a1", UserID: "u1", Status: chat.StatusActive, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *stubChatService) Get(context.Context, string) (*chat.Chat, error) {
	now := time.Now().UTC()
	return &chat.Chat{ID: "c1", AgentID: "a1", UserID: "u1", Status: chat.StatusActive, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *stubChatService) SubscribeRuntimeEvents(context.Context, string) (<-chan orchdomain.RuntimeEvent, error) {
	ch := make(chan orchdomain.RuntimeEvent)
	return ch, nil
}

func (s *stubChatService) Close(context.Context, string) error { return nil }

const testJWTSecret = "test-secret-key-for-chat-handler-tests"

func testJWTManager() *jwt.Manager {
	return jwt.NewManager(testJWTSecret, time.Hour)
}

func testToken(jwtMgr *jwt.Manager, userID, role string) string {
	token, _, err := jwtMgr.GenerateAccessToken(userID, role)
	if err != nil {
		panic(err)
	}
	return token
}

func TestSendMessage_Unauthenticated(t *testing.T) {
	svc := &stubChatService{}
	jwtMgr := testJWTManager()
	h := NewHandler(svc, jwtMgr)

	r := chi.NewRouter()
	h.RegisterRoutes(r)

	body, _ := json.Marshal(map[string]any{"message": "hello"})
	req := httptest.NewRequest(http.MethodPost, "/chat/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestSendMessage_AutoCreatesConversation(t *testing.T) {
	svc := &stubChatService{}
	jwtMgr := testJWTManager()
	h := NewHandler(svc, jwtMgr)

	r := chi.NewRouter()
	h.RegisterRoutes(r)

	body, _ := json.Marshal(map[string]any{
		"message": "hello",
	})

	req := httptest.NewRequest(http.MethodPost, "/chat/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken(jwtMgr, "user-1", "user"))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if svc.lastContent != "hello" {
		t.Fatalf("expected message %q, got %q", "hello", svc.lastContent)
	}
	if svc.lastID != "" {
		t.Fatalf("expected empty id for auto-create, got %q", svc.lastID)
	}
	if svc.lastParams.UserID != "user-1" {
		t.Fatalf("expected user_id %q, got %q", "user-1", svc.lastParams.UserID)
	}
}

func TestSendMessage_ContinuesExistingConversation(t *testing.T) {
	svc := &stubChatService{}
	jwtMgr := testJWTManager()
	h := NewHandler(svc, jwtMgr)

	r := chi.NewRouter()
	h.RegisterRoutes(r)

	body, _ := json.Marshal(map[string]any{
		"message":         "follow-up",
		"conversation_id": "existing-id",
	})

	req := httptest.NewRequest(http.MethodPost, "/chat/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken(jwtMgr, "user-1", "user"))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if svc.lastID != "existing-id" {
		t.Fatalf("expected id %q, got %q", "existing-id", svc.lastID)
	}
}

func TestSendMessage_PassesAgentConfig(t *testing.T) {
	svc := &stubChatService{}
	jwtMgr := testJWTManager()
	h := NewHandler(svc, jwtMgr)

	r := chi.NewRouter()
	h.RegisterRoutes(r)

	body, _ := json.Marshal(map[string]any{
		"message":       "hello",
		"agent_id":      "agent-42",
		"model":         "gpt-4",
		"system_prompt": "You are helpful",
	})

	req := httptest.NewRequest(http.MethodPost, "/chat/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken(jwtMgr, "user-1", "admin"))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if svc.lastParams.AgentID != "agent-42" {
		t.Fatalf("expected agent_id %q, got %q", "agent-42", svc.lastParams.AgentID)
	}
	if svc.lastParams.Model != "gpt-4" {
		t.Fatalf("expected model %q, got %q", "gpt-4", svc.lastParams.Model)
	}
	if svc.lastParams.SystemPrompt != "You are helpful" {
		t.Fatalf("expected system_prompt %q, got %q", "You are helpful", svc.lastParams.SystemPrompt)
	}
}

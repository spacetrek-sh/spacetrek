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
	lastID              string
	lastContent         string
	lastParams          chat.CreateParams
	lastListParams      chat.ListParams
	listResult          *chat.ListResult
	lastListMsgParams   chat.ListMessagesParams
	listMessagesResult  *chat.ListMessagesResult
}

func (s *stubChatService) ListConversations(_ context.Context, params chat.ListParams) (*chat.ListResult, error) {
	s.lastListParams = params
	if s.listResult != nil {
		return s.listResult, nil
	}
	return &chat.ListResult{Items: []*chat.ConversationSummary{}}, nil
}

func (s *stubChatService) ListMessages(_ context.Context, params chat.ListMessagesParams) (*chat.ListMessagesResult, error) {
	s.lastListMsgParams = params
	if s.listMessagesResult != nil {
		return s.listMessagesResult, nil
	}
	return &chat.ListMessagesResult{Items: []*chat.TimelineEntry{}}, nil
}

func (s *stubChatService) SendOrCreate(_ context.Context, id, content string, p chat.CreateParams) (*chat.Chat, error) {
	s.lastID = id
	s.lastContent = content
	s.lastParams = p
	now := time.Now().UTC()
	return &chat.Chat{ID: "c1", AgentID: "a1", UserID: "u1", Status: chat.StatusActive, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *stubChatService) SendOrCreateAsync(_ context.Context, id, content string, p chat.CreateParams) (string, error) {
	s.lastID = id
	s.lastContent = content
	s.lastParams = p
	return "c1", nil
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

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
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

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
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

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
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

func TestList_Unauthenticated(t *testing.T) {
	svc := &stubChatService{}
	jwtMgr := testJWTManager()
	h := NewHandler(svc, jwtMgr)

	r := chi.NewRouter()
	h.RegisterRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/chat/", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestList_DefaultPagination(t *testing.T) {
	svc := &stubChatService{}
	jwtMgr := testJWTManager()
	h := NewHandler(svc, jwtMgr)

	r := chi.NewRouter()
	h.RegisterRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/chat/", nil)
	req.Header.Set("Authorization", "Bearer "+testToken(jwtMgr, "user-1", "user"))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if svc.lastListParams.UserID != "user-1" {
		t.Fatalf("expected user_id %q, got %q", "user-1", svc.lastListParams.UserID)
	}
	if svc.lastListParams.Limit != 20 {
		t.Fatalf("expected default limit 20, got %d", svc.lastListParams.Limit)
	}
	if svc.lastListParams.Cursor != nil {
		t.Fatalf("expected nil cursor for first page, got %+v", svc.lastListParams.Cursor)
	}
}

func TestList_WithCursor(t *testing.T) {
	svc := &stubChatService{}
	jwtMgr := testJWTManager()
	h := NewHandler(svc, jwtMgr)

	r := chi.NewRouter()
	h.RegisterRoutes(r)

	cursor := encodeCursor(&chat.ListCursor{
		CreatedAt: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC),
		ID:        "550e8400-e29b-41d4-a716-446655440000",
	})

	req := httptest.NewRequest(http.MethodGet, "/chat/?cursor="+cursor+"&limit=5", nil)
	req.Header.Set("Authorization", "Bearer "+testToken(jwtMgr, "user-1", "user"))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if svc.lastListParams.Limit != 5 {
		t.Fatalf("expected limit 5, got %d", svc.lastListParams.Limit)
	}
	if svc.lastListParams.Cursor == nil {
		t.Fatal("expected cursor to be decoded, got nil")
	}
	if svc.lastListParams.Cursor.ID != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("expected cursor ID %q, got %q", "550e8400-e29b-41d4-a716-446655440000", svc.lastListParams.Cursor.ID)
	}
}

func TestList_InvalidCursor(t *testing.T) {
	svc := &stubChatService{}
	jwtMgr := testJWTManager()
	h := NewHandler(svc, jwtMgr)

	r := chi.NewRouter()
	h.RegisterRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/chat/?cursor=not-valid-base64!!!&limit=10", nil)
	req.Header.Set("Authorization", "Bearer "+testToken(jwtMgr, "user-1", "user"))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if svc.lastListParams.Cursor != nil {
		t.Fatalf("expected nil cursor for invalid input, got %+v", svc.lastListParams.Cursor)
	}
}

func TestList_LimitClamped(t *testing.T) {
	svc := &stubChatService{}
	jwtMgr := testJWTManager()
	h := NewHandler(svc, jwtMgr)

	r := chi.NewRouter()
	h.RegisterRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/chat/?limit=999", nil)
	req.Header.Set("Authorization", "Bearer "+testToken(jwtMgr, "user-1", "user"))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if svc.lastListParams.Limit != 20 {
		t.Fatalf("expected limit falling back to default 20 for out-of-range value, got %d", svc.lastListParams.Limit)
	}
}

func TestListMessages_Unauthenticated(t *testing.T) {
	svc := &stubChatService{}
	jwtMgr := testJWTManager()
	h := NewHandler(svc, jwtMgr)

	r := chi.NewRouter()
	h.RegisterRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/chat/chat-1/messages", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", w.Code, w.Body.String())
	}
}

func TestListMessages_DefaultPagination(t *testing.T) {
	svc := &stubChatService{}
	jwtMgr := testJWTManager()
	h := NewHandler(svc, jwtMgr)

	r := chi.NewRouter()
	h.RegisterRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/chat/chat-1/messages", nil)
	req.Header.Set("Authorization", "Bearer "+testToken(jwtMgr, "user-1", "user"))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if svc.lastListMsgParams.ChatID != "chat-1" {
		t.Fatalf("expected chat_id %q, got %q", "chat-1", svc.lastListMsgParams.ChatID)
	}
	if svc.lastListMsgParams.Limit != 50 {
		t.Fatalf("expected default limit 50, got %d", svc.lastListMsgParams.Limit)
	}
	if svc.lastListMsgParams.Cursor != nil {
		t.Fatalf("expected nil cursor, got %+v", svc.lastListMsgParams.Cursor)
	}
}

func TestListMessages_WithCursor(t *testing.T) {
	svc := &stubChatService{}
	jwtMgr := testJWTManager()
	h := NewHandler(svc, jwtMgr)

	r := chi.NewRouter()
	h.RegisterRoutes(r)

	cursor := encodeMessageCursor(&chat.MessageCursor{Timestamp: time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)})

	req := httptest.NewRequest(http.MethodGet, "/chat/chat-1/messages?cursor="+cursor+"&limit=10", nil)
	req.Header.Set("Authorization", "Bearer "+testToken(jwtMgr, "user-1", "user"))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if svc.lastListMsgParams.Limit != 10 {
		t.Fatalf("expected limit 10, got %d", svc.lastListMsgParams.Limit)
	}
	if svc.lastListMsgParams.Cursor == nil {
		t.Fatal("expected cursor to be decoded, got nil")
	}
	if !svc.lastListMsgParams.Cursor.Timestamp.Equal(time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)) {
		t.Fatalf("expected cursor timestamp 2024-01-15T10:30:00Z, got %s", svc.lastListMsgParams.Cursor.Timestamp)
	}
}

func TestListMessages_InvalidCursor(t *testing.T) {
	svc := &stubChatService{}
	jwtMgr := testJWTManager()
	h := NewHandler(svc, jwtMgr)

	r := chi.NewRouter()
	h.RegisterRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/chat/chat-1/messages?cursor=garbage!!!", nil)
	req.Header.Set("Authorization", "Bearer "+testToken(jwtMgr, "user-1", "user"))
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if svc.lastListMsgParams.Cursor != nil {
		t.Fatalf("expected nil cursor for invalid input, got %+v", svc.lastListMsgParams.Cursor)
	}
}

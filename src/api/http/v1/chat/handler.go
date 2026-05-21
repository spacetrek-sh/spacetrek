package chathttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/spacetrek-sh/spacetrek/pkg/auth/jwt"
	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	httputil "github.com/spacetrek-sh/spacetrek/pkg/http"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/chat"
	orchdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/orchestrator"
	"github.com/spacetrek-sh/spacetrek/src/middleware"
)

// chatService is the local dependency interface for the handler.
// chatsvc.Service satisfies this automatically.
type chatService interface {
	SendOrCreateAsync(ctx context.Context, id, content string, p chat.CreateParams) (string, error)
	Get(ctx context.Context, id string) (*chat.Chat, error)
	ListConversations(ctx context.Context, params chat.ListParams) (*chat.ListResult, error)
	ListMessages(ctx context.Context, params chat.ListMessagesParams) (*chat.ListMessagesResult, error)
	SubscribeRuntimeEvents(ctx context.Context, chatID string) (<-chan orchdomain.RuntimeEvent, error)
	Close(ctx context.Context, id string) error
}

// Handler groups all chat-related HTTP handlers.
type Handler struct {
	svc        chatService
	jwtManager *jwt.Manager
}

func NewHandler(svc chatService, jwtManager *jwt.Manager) *Handler {
	return &Handler{svc: svc, jwtManager: jwtManager}
}

// RegisterRoutes registers all chat routes under the given chi.Router.
// All chat routes require authentication (admin or user role).
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/chat", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticate(h.jwtManager))
			r.Post("/", h.SendMessage)
			r.Get("/", h.List)
			r.Get("/{id}", h.Get)
			r.Get("/{id}/messages", h.ListMessages)
			r.Get("/{id}/stream", h.Stream)
			r.Delete("/{id}", h.Close)
		})
	})
}

// SendMessage handles POST /api/v1/chat
func (h *Handler) SendMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	var req sendMessageRequest
	if err := httputil.BindJSON(r, &req); err != nil {
		logger.WarnContext(ctx, "chat message failed", "error", err)
		httputil.WriteError(w, err)
		return
	}

	userID := middleware.GetUserID(ctx)

	logger.DebugContext(ctx, "chat message requested",
		"conversation_id", req.ConversationID,
		"message_len", len(req.Message),
		"agent_id", req.AgentID,
		"user_id", userID)

	chatID, err := h.svc.SendOrCreateAsync(ctx, req.ConversationID, req.Message, chat.CreateParams{
		UserID:       userID,
		AgentID:      req.AgentID,
		Model:        req.Model,
		SystemPrompt: req.SystemPrompt,
	})
	if err != nil {
		logger.WarnContext(ctx, "chat message failed",
			"conversation_id", req.ConversationID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	statusCode := http.StatusAccepted

	logger.InfoContext(ctx, "chat message accepted",
		"chat_id", chatID)
	httputil.WriteJSON(w, statusCode, "message accepted", messageAcceptedResponse{
		ChatID: chatID,
		Status: "processing",
	})
}

// Get handles GET /api/v1/chat/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing chat id"))
		return
	}

	logger.DebugContext(ctx, "chat get requested", "chat_id", id)

	c, err := h.svc.Get(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "chat retrieval failed", "chat_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.DebugContext(ctx, "chat retrieved", "chat_id", id, "status", c.Status, "message_count", len(c.Messages))
	httputil.WriteJSON(w, http.StatusOK, "chat retrieved", toResponse(c))
}

// List handles GET /api/v1/chat and returns conversations for the authenticated user.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	userID := middleware.GetUserID(ctx)
	if userID == "" {
		httputil.WriteError(w, exception.Unauthorized("user not authenticated"))
		return
	}

	params := chat.ListParams{
		UserID: userID,
		Limit:  parseListLimit(r),
		Cursor: parseCursor(r),
	}

	result, err := h.svc.ListConversations(ctx, params)
	if err != nil {
		logger.WarnContext(ctx, "list conversations failed", "user_id", userID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, "conversations retrieved", toListResponse(result))
}

// ListMessages handles GET /api/v1/chat/{id}/messages and returns paginated messages.
func (h *Handler) ListMessages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	chatID := chi.URLParam(r, "id")
	if chatID == "" {
		httputil.WriteError(w, exception.BadRequest("missing chat id"))
		return
	}

	params := chat.ListMessagesParams{
		ChatID: chatID,
		Limit:  parseMessagesLimit(r),
		Cursor: parseMessageCursor(r),
	}

	result, err := h.svc.ListMessages(ctx, params)
	if err != nil {
		logger.WarnContext(ctx, "list messages failed", "chat_id", chatID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, "messages retrieved", toListMessagesResponse(result))
}

func parseListLimit(r *http.Request) int {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	return limit
}

func parseCursor(r *http.Request) *chat.ListCursor {
	encoded := r.URL.Query().Get("cursor")
	if encoded == "" {
		return nil
	}

	decoded, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}

	var raw struct {
		CreatedAt string `json:"c"`
		ID        string `json:"i"`
	}
	if err := json.Unmarshal(decoded, &raw); err != nil {
		return nil
	}

	ts, err := time.Parse(time.RFC3339Nano, raw.CreatedAt)
	if err != nil {
		return nil
	}

	return &chat.ListCursor{
		CreatedAt: ts,
		ID:        raw.ID,
	}
}

func encodeCursor(c *chat.ListCursor) string {
	if c == nil {
		return ""
	}
	raw := struct {
		CreatedAt string `json:"c"`
		ID        string `json:"i"`
	}{
		CreatedAt: c.CreatedAt.Format(time.RFC3339Nano),
		ID:        c.ID,
	}
	b, _ := json.Marshal(raw)
	return base64.URLEncoding.EncodeToString(b)
}

func toListResponse(result *chat.ListResult) *listConversationsResponse {
	items := make([]conversationSummaryResponse, len(result.Items))
	for i, s := range result.Items {
		items[i] = conversationSummaryResponse{
			ID:            s.ID,
			AgentID:       s.AgentID,
			UserID:        s.UserID,
			Title:         s.Title,
			VMID:          s.VMID,
			Status:        string(s.Status),
			LastMessage:   s.LastMessage,
			LastMessageAt: s.LastMessageAt,
			CreatedAt:     s.CreatedAt,
			UpdatedAt:     s.UpdatedAt,
		}
	}
	resp := &listConversationsResponse{
		Conversations: items,
		HasMore:       result.NextCursor != nil,
	}
	if result.NextCursor != nil {
		resp.NextCursor = encodeCursor(result.NextCursor)
	}
	return resp
}

func parseMessagesLimit(r *http.Request) int {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	return limit
}

func parseMessageCursor(r *http.Request) *chat.MessageCursor {
	encoded := r.URL.Query().Get("cursor")
	if encoded == "" {
		return nil
	}

	decoded, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		return nil
	}

	var raw struct {
		T string `json:"t"`
	}
	if err := json.Unmarshal(decoded, &raw); err != nil {
		return nil
	}
	ts, err := time.Parse(time.RFC3339Nano, raw.T)
	if err != nil {
		return nil
	}

	return &chat.MessageCursor{Timestamp: ts}
}

func encodeMessageCursor(c *chat.MessageCursor) string {
	if c == nil {
		return ""
	}
	raw := struct {
		T string `json:"t"`
	}{T: c.Timestamp.Format(time.RFC3339Nano)}
	b, _ := json.Marshal(raw)
	return base64.URLEncoding.EncodeToString(b)
}

func toListMessagesResponse(result *chat.ListMessagesResult) *listMessagesResponse {
	items := make([]messageSummaryResponse, len(result.Items))
	for i, m := range result.Items {
		items[i] = messageSummaryResponse{
			ID:             m.ID,
			Source:         m.Source,
			SequenceNumber: m.SequenceNumber,
			Role:           string(m.Role),
			EventType:      m.EventType,
			Step:           m.Step,
			Content:        m.Content,
			ContentType:    string(m.ContentType),
			Metadata:       m.Metadata,
			At:             m.At,
		}
	}
	resp := &listMessagesResponse{
		Messages: items,
		HasMore:  result.NextCursor != nil,
	}
	if result.NextCursor != nil {
		resp.NextCursor = encodeMessageCursor(result.NextCursor)
	}
	return resp
}

// Stream handles GET /api/v1/chat/{id}/stream using server-sent events.
func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing chat id"))
		return
	}

	logger.DebugContext(ctx, "chat stream requested", "chat_id", id)

	events, err := h.svc.SubscribeRuntimeEvents(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "chat stream subscription failed", "chat_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.DebugContext(ctx, "chat stream opened", "chat_id", id)
	httputil.PrepareSSE(w)
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if err := httputil.WriteSSEHeartbeat(w); err != nil {
				return
			}
			_ = rc.Flush()
		case event, ok := <-events:
			if !ok {
				return
			}
			if err := httputil.WriteSSEEvent(w, string(event.Type), event); err != nil {
				return
			}
			_ = rc.Flush()
		}
	}
}

// Close handles DELETE /api/v1/chat/{id}
func (h *Handler) Close(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing chat id"))
		return
	}

	logger.DebugContext(ctx, "chat close requested", "chat_id", id)

	if err := h.svc.Close(ctx, id); err != nil {
		logger.WarnContext(ctx, "chat close failed", "chat_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "chat closed", "chat_id", id)
	httputil.NoContent(w)
}

func toResponse(c *chat.Chat) *chatResponse {
	msgs := make([]messageResponse, len(c.Messages))
	for i, m := range c.Messages {
		msgs[i] = messageResponse{
			Role:        string(m.Role),
			Content:     m.Content,
			ContentType: string(m.ContentType),
			Metadata:    m.Metadata,
			At:          m.At,
		}
	}
	return &chatResponse{
		ID:        c.ID,
		AgentID:   c.AgentID,
		UserID:    c.UserID,
		Status:    string(c.Status),
		Messages:  msgs,
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}

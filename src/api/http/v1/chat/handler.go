package chathttp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kumori-sh/spacetrk/pkg/auth/jwt"
	"github.com/kumori-sh/spacetrk/pkg/exception"
	httputil "github.com/kumori-sh/spacetrk/pkg/http"
	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	orchdomain "github.com/kumori-sh/spacetrk/src/core/domain/orchestrator"
	"github.com/kumori-sh/spacetrk/src/core/domain/chat"
	"github.com/kumori-sh/spacetrk/src/middleware"
)

// chatService is the local dependency interface for the handler.
// chatsvc.Service satisfies this automatically.
type chatService interface {
	SendOrCreate(ctx context.Context, id, content string, p chat.CreateParams) (*chat.Chat, error)
	Get(ctx context.Context, id string) (*chat.Chat, error)
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
			r.Get("/{id}", h.Get)
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

	c, err := h.svc.SendOrCreate(ctx, req.ConversationID, req.Message, chat.CreateParams{
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

	statusCode := http.StatusOK
	if req.ConversationID == "" {
		statusCode = http.StatusCreated
	}

	logger.InfoContext(ctx, "chat message processed",
		"chat_id", c.ID, "message_count", len(c.Messages))
	httputil.WriteJSON(w, statusCode, "message sent", toResponse(c))
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
	prepareSSE(w)
	rc := http.NewResponseController(w)
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			writeSSEEvent(w, "heartbeat", map[string]any{"ts": time.Now().UTC()})
			_ = rc.Flush()
		case event, ok := <-events:
			if !ok {
				return
			}
			writeSSEEvent(w, string(event.Type), event)
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
			Role:    string(m.Role),
			Content: m.Content,
			At:      m.At,
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

func prepareSSE(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")

	_ = http.NewResponseController(w).Flush()
}

func writeSSEEvent(w http.ResponseWriter, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = w.Write([]byte("event: " + event + "\n"))
	_, _ = w.Write([]byte("data: " + string(data) + "\n\n"))
}

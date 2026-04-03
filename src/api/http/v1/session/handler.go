package sessionhttp

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kumori-sh/spacetrk/pkg/exception"
	httputil "github.com/kumori-sh/spacetrk/pkg/http"
	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	orchdomain "github.com/kumori-sh/spacetrk/src/core/domain/orchestrator"
	"github.com/kumori-sh/spacetrk/src/core/domain/session"
)

// sessionService is the local dependency interface for the handler.
// sessionsvc.Service satisfies this automatically.
type sessionService interface {
	Create(ctx context.Context, p session.CreateParams) (*session.Session, error)
	Get(ctx context.Context, id string) (*session.Session, error)
	SendMessage(ctx context.Context, id, content, vmID string) (*session.Session, error)
	SubscribeRuntimeEvents(ctx context.Context, sessionID string) (<-chan orchdomain.RuntimeEvent, error)
	Close(ctx context.Context, id string) error
}

// Handler groups all session-related HTTP handlers.
type Handler struct {
	svc sessionService
}

func NewHandler(svc sessionService) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers all session routes under the given chi.Router.
// The router is expected to already be under /api/v1, so routes are
// registered under /sessions/{id}, etc.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/sessions", func(r chi.Router) {
		r.Post("/", h.Create)
		r.Get("/{id}", h.Get)
		r.Get("/{id}/stream", h.Stream)
		r.Post("/{id}/messages", h.SendMessage)
		r.Delete("/{id}", h.Close)
	})
}

// Create handles POST /api/v1/sessions
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	var req createSessionRequest
	if err := httputil.BindJSON(r, &req); err != nil {
		logger.WarnContext(ctx, "session creation failed", "error", err)
		httputil.WriteError(w, err)
		return
	}

	s, err := h.svc.Create(ctx, session.CreateParams{
		AgentID:      req.AgentID,
		UserID:       req.UserID,
		AgentName:    req.AgentName,
		Model:        req.Model,
		SystemPrompt: req.SystemPrompt,
	})
	if err != nil {
		logger.WarnContext(ctx, "session creation failed", "agent_id", req.AgentID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "session created", "session_id", s.ID, "agent_id", s.AgentID)
	httputil.Created(w, "session created", toResponse(s))
}

// Get handles GET /api/v1/sessions/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing session id"))
		return
	}

	s, err := h.svc.Get(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "session retrieval failed", "session_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, "session retrieved", toResponse(s))
}

// SendMessage handles POST /api/v1/sessions/{id}/messages
func (h *Handler) SendMessage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing session id"))
		return
	}

	var req sendMessageRequest
	if err := httputil.BindJSON(r, &req); err != nil {
		logger.WarnContext(ctx, "send message failed", "session_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	s, err := h.svc.SendMessage(ctx, id, req.Content, req.VMID)
	if err != nil {
		logger.WarnContext(ctx, "send message failed", "session_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "message sent", "session_id", id)
	httputil.WriteJSON(w, http.StatusOK, "message sent", toResponse(s))
}

// Stream handles GET /api/v1/sessions/{id}/stream using server-sent events.
func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing session id"))
		return
	}

	events, err := h.svc.SubscribeRuntimeEvents(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "session stream subscription failed", "session_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.DebugContext(ctx, "session stream opened", "session_id", id)
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

// Close handles DELETE /api/v1/sessions/{id}
func (h *Handler) Close(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing session id"))
		return
	}

	if err := h.svc.Close(ctx, id); err != nil {
		logger.WarnContext(ctx, "session close failed", "session_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "session closed", "session_id", id)
	httputil.NoContent(w)
}

func toResponse(s *session.Session) *sessionResponse {
	msgs := make([]messageResponse, len(s.Messages))
	for i, m := range s.Messages {
		msgs[i] = messageResponse{
			Role:    string(m.Role),
			Content: m.Content,
			At:      m.At,
		}
	}
	return &sessionResponse{
		ID:        s.ID,
		AgentID:   s.AgentID,
		UserID:    s.UserID,
		Status:    string(s.Status),
		Messages:  msgs,
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
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

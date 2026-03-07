package sessionhttp

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/kumori-sh/spacetrk/pkg/exception"
	httputil "github.com/kumori-sh/spacetrk/pkg/http"
	"github.com/kumori-sh/spacetrk/src/core/domain/session"
)

// sessionService is the local dependency interface for the handler.
// sessionsvc.Service satisfies this automatically.
type sessionService interface {
	Create(ctx context.Context, p session.CreateParams) (*session.Session, error)
	Get(ctx context.Context, id string) (*session.Session, error)
	SendMessage(ctx context.Context, id, content string) (*session.Session, error)
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
		r.Post("/{id}/messages", h.SendMessage)
		r.Delete("/{id}", h.Close)
	})
}

// Create handles POST /api/v1/sessions
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	var req createSessionRequest
	if err := httputil.BindJSON(r, &req); err != nil {
		httputil.WriteError(w, err)
		return
	}

	s, err := h.svc.Create(r.Context(), session.CreateParams{
		AgentID: req.AgentID,
		UserID:  req.UserID,
	})
	if err != nil {
		httputil.WriteError(w, err)
		return
	}

	httputil.Created(w, "session created", toResponse(s))
}

// Get handles GET /api/v1/sessions/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing session id"))
		return
	}

	s, err := h.svc.Get(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, "session retrieved", toResponse(s))
}

// SendMessage handles POST /api/v1/sessions/{id}/messages
func (h *Handler) SendMessage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing session id"))
		return
	}

	var req sendMessageRequest
	if err := httputil.BindJSON(r, &req); err != nil {
		httputil.WriteError(w, err)
		return
	}

	s, err := h.svc.SendMessage(r.Context(), id, req.Content)
	if err != nil {
		httputil.WriteError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, "message sent", toResponse(s))
}

// Close handles DELETE /api/v1/sessions/{id}
func (h *Handler) Close(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing session id"))
		return
	}

	if err := h.svc.Close(r.Context(), id); err != nil {
		httputil.WriteError(w, err)
		return
	}

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

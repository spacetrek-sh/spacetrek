package agenthttp

import (
	"context"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/spacetrek-sh/spacetrek/pkg/auth/jwt"
	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	httputil "github.com/spacetrek-sh/spacetrek/pkg/http"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/agent"
	"github.com/spacetrek-sh/spacetrek/src/middleware"
)

// agentService is the local dependency interface for the handler.
// agentsvc.Service satisfies this automatically.
type agentService interface {
	Create(ctx context.Context, p agent.CreateParams) (*agent.Agent, error)
	Get(ctx context.Context, id string) (*agent.Agent, error)
	List(ctx context.Context, offset, limit int) ([]*agent.Agent, int64, error)
	Update(ctx context.Context, id string, p agent.UpdateParams) (*agent.Agent, error)
	Delete(ctx context.Context, id string) error
}

// Handler groups all agent-related HTTP handlers.
type Handler struct {
	svc        agentService
	jwtManager *jwt.Manager
}

func NewHandler(svc agentService, jwtManager *jwt.Manager) *Handler {
	return &Handler{svc: svc, jwtManager: jwtManager}
}

// RegisterRoutes registers all agent routes under the given chi.Router.
// The router is expected to already be under /api/v1, so routes are
// registered under /agents/{id}, etc.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/agents", func(r chi.Router) {
		// Authenticated routes — admin and user roles
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticate(h.jwtManager))
			r.Post("/", h.Create)
			r.Get("/", h.List)
			r.Get("/{id}", h.Get)
			r.Put("/{id}", h.Update)
			r.Delete("/{id}", h.Delete)
		})
	})
}

// Create handles POST /api/v1/agents
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	var req createAgentRequest
	if err := httputil.BindJSON(r, &req); err != nil {
		logger.WarnContext(ctx, "agent creation failed", "error", err)
		httputil.WriteError(w, err)
		return
	}

	userID := middleware.GetUserID(ctx)

	a, err := h.svc.Create(ctx, agent.CreateParams{
		UserID:       userID,
		Name:         req.Name,
		Description:  req.Description,
		Model:        req.Model,
		SystemPrompt: req.SystemPrompt,
	})
	if err != nil {
		logger.WarnContext(ctx, "agent creation failed", "name", req.Name, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "agent created", "agent_id", a.ID, "name", a.Name)
	httputil.Created(w, "agent created", toResponse(a))
}

// Get handles GET /api/v1/agents/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing agent id"))
		return
	}

	a, err := h.svc.Get(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "agent retrieval failed", "agent_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, "agent retrieved", toResponse(a))
}

// List handles GET /api/v1/agents?offset=0&limit=20
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	offset, limit := parsePagination(r)

	agents, total, err := h.svc.List(ctx, offset, limit)
	if err != nil {
		logger.WarnContext(ctx, "agent list failed", "offset", offset, "limit", limit, "error", err)
		httputil.WriteError(w, err)
		return
	}

	resp := &listAgentsResponse{
		Agents: make([]*agentResponse, len(agents)),
		Total:  total,
		Offset: offset,
		Limit:  limit,
	}
	for i, a := range agents {
		resp.Agents[i] = toResponse(a)
	}

	httputil.WriteJSON(w, http.StatusOK, "agents retrieved", resp)
}

// Update handles PUT /api/v1/agents/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing agent id"))
		return
	}

	var req updateAgentRequest
	if err := httputil.BindJSON(r, &req); err != nil {
		logger.WarnContext(ctx, "agent update failed", "agent_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	a, err := h.svc.Update(ctx, id, agent.UpdateParams{
		Name:         req.Name,
		Description:  req.Description,
		Model:        req.Model,
		SystemPrompt: req.SystemPrompt,
	})
	if err != nil {
		logger.WarnContext(ctx, "agent update failed", "agent_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "agent updated", "agent_id", id)
	httputil.WriteJSON(w, http.StatusOK, "agent updated", toResponse(a))
}

// Delete handles DELETE /api/v1/agents/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing agent id"))
		return
	}

	if err := h.svc.Delete(ctx, id); err != nil {
		logger.WarnContext(ctx, "agent deletion failed", "agent_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "agent deleted", "agent_id", id)
	httputil.NoContent(w)
}

// toResponse converts a domain Agent into its JSON representation.
func toResponse(a *agent.Agent) *agentResponse {
	return &agentResponse{
		ID:           a.ID,
		Name:         a.Name,
		Description:  a.Description,
		Model:        a.Model,
		SystemPrompt: a.SystemPrompt,
		Status:       string(a.Status),
		CreatedAt:    a.CreatedAt,
		UpdatedAt:    a.UpdatedAt,
	}
}

// parsePagination extracts ?offset and ?limit query params with safe defaults.
func parsePagination(r *http.Request) (offset, limit int) {
	offset, limit = 0, 20
	q := r.URL.Query()
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	return
}

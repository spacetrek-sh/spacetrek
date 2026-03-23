// Package vm provides HTTP handlers and DTOs for VM endpoints.
package vm

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kumori-sh/spacetrk/pkg/auth/jwt"
	"github.com/kumori-sh/spacetrk/pkg/exception"
	httputil "github.com/kumori-sh/spacetrk/pkg/http"
	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	"github.com/kumori-sh/spacetrk/src/core/domain/environment"
	"github.com/kumori-sh/spacetrk/src/middleware"
	vmdomain "github.com/kumori-sh/spacetrk/src/core/domain/vm"
	vmservice "github.com/kumori-sh/spacetrk/src/service/vm"
)

// Handler groups all VM-related HTTP handlers.
type Handler struct {
	vmservice  *vmservice.Service
	jwtManager *jwt.Manager
	envRepo    EnvironmentRepository
}

// EnvironmentRepository defines the interface for fetching environment details.
type EnvironmentRepository interface {
	GetByID(ctx context.Context, id string) (*environment.Environment, error)
}

// NewHandler creates a new VM handler.
func NewHandler(vmSvc *vmservice.Service, jwtMgr *jwt.Manager, envRepo EnvironmentRepository) *Handler {
	return &Handler{
		vmservice:  vmSvc,
		jwtManager: jwtMgr,
		envRepo:    envRepo,
	}
}

// RegisterRoutes registers all VM routes under the given router.
// All VM routes require admin role.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/vm", func(r chi.Router) {
		// Admin-only routes
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticate(h.jwtManager))
			r.Use(middleware.RequireRole("admin"))
			r.Post("/", h.Create)
			r.Get("/{id}", h.Get)
			r.Delete("/{id}", h.Stop)
			r.Delete("/{id}/destroy", h.Destroy)
			r.Post("/{id}/execute", h.ExecuteCommand)
		})
	})
}

// Create handles POST /api/v1/vm. Creates a new VM with the specified parameters.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	var req createVMRequest
	if err := httputil.BindJSON(r, &req); err != nil {
		logger.WarnContext(ctx, "VM creation failed", "error", err)
		httputil.WriteError(w, err)
		return
	}

	// Parse provider
	var provider vmdomain.Provider
	if req.Provider != "" {
		provider = vmdomain.Provider(req.Provider)
	}

	vm, err := h.vmservice.Create(ctx, req.EnvironmentID, provider, req.VCPU, req.MemoryMB, req.DiskMB)
	if err != nil {
		logger.WarnContext(ctx, "VM creation failed", "env_id", req.EnvironmentID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	// Fetch environment to compute effective resources
	env, err := h.envRepo.GetByID(ctx, vm.EnvironmentID)
	if err != nil {
		logger.ErrorContext(ctx, "failed to fetch environment for response", "env_id", vm.EnvironmentID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "VM created", "vm_id", vm.ID, "env_id", req.EnvironmentID)
	httputil.Created(w, "VM created", toCreateVMResponse(vm, env))
}

// Get handles GET /api/v1/vm/{id}. Retrieves details of the specified VM.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing VM ID"))
		return
	}

	vm, err := h.vmservice.Get(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "VM retrieval failed", "vm_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, "VM retrieved", toGetVMResponse(vm))
}

// Stop handles DELETE /api/v1/vm/{id}. Stops the specified VM.
func (h *Handler) Stop(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing VM ID"))
		return
	}

	vm, err := h.vmservice.Stop(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "VM stop failed", "vm_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "VM stopped", "vm_id", id)
	httputil.WriteJSON(w, http.StatusOK, "VM stopped", deleteVMResponse{ID: vm.ID})
}

// Destroy handles DELETE /api/v1/vm/{id}/destroy. Destroys the specified VM.
func (h *Handler) Destroy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing VM ID"))
		return
	}

	if err := h.vmservice.Destroy(ctx, id); err != nil {
		logger.WarnContext(ctx, "VM destruction failed", "vm_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "VM destroyed", "vm_id", id)
	httputil.WriteJSON(w, http.StatusOK, "VM destroyed", deleteVMResponse{ID: id})
}

// ExecuteCommand handles POST /api/v1/vm/{id}/execute. Executes a command on the specified VM.
func (h *Handler) ExecuteCommand(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing VM ID"))
		return
	}

	var req executeCommandRequest
	if err := httputil.BindJSON(r, &req); err != nil {
		logger.WarnContext(ctx, "command execution failed", "vm_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	output, err := h.vmservice.ExecuteCommand(ctx, id, req.Command)
	if err != nil {
		logger.WarnContext(ctx, "command execution failed", "vm_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "command executed", "vm_id", id, "command", req.Command)
	httputil.WriteJSON(w, http.StatusOK, "command executed", executeCommandResponse{Output: output})
}

// toCreateVMResponse converts a domain VM to createVMResponse.
func toCreateVMResponse(vm *vmdomain.VM, env *environment.Environment) createVMResponse {
	return createVMResponse{
		ID:            vm.ID,
		EnvironmentID: vm.EnvironmentID,
		Provider:      string(vm.Provider),
		Status:        string(vm.Status),
		VCPU:          vm.GetVCPU(env.GetVCPU()),
		MemoryMB:      vm.GetMemoryMB(env.GetMemoryMB()),
		DiskMB:        vm.GetDiskMB(env.GetDiskMB()),
	}
}

// toGetVMResponse converts a domain VM to getVMResponse.
func toGetVMResponse(vm *vmdomain.VM) getVMResponse {
	return getVMResponse{
		ID:            vm.ID,
		EnvironmentID: vm.EnvironmentID,
		Provider:      string(vm.Provider),
		Status:        string(vm.Status),
		VCPU:          vm.VCPU,
		MemoryMB:      vm.MemoryMB,
		DiskMB:        vm.DiskMB,
		HasOverrides:  vm.HasCustomResources(),
		IPAddress:     vm.IPAddress,
		ChatID:        vm.ChatID,
		CreatedAt:     vm.CreatedAt.Format("2006-01-02T15:04:05Z"),
		TerminatedAt:  formatTimePtr(vm.TerminatedAt),
		AssignedAt:    formatTimePtr(vm.AssignedAt),
	}
}

// formatTimePtr formats a time pointer to ISO 8601 string, or empty if nil.
func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := t.Format("2006-01-02T15:04:05Z")
	return &formatted
}

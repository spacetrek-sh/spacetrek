// Package vm exposes the internal VM operations consumed by the
// spacetrek-activator container during request-driven cold-starts.
// These handlers are NOT mounted on the public API and have no auth —
// the only caller is the activator, reachable via the shared-netns
// localhost loopback.
package vm

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	httputil "github.com/spacetrek-sh/spacetrek/pkg/http"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
	vmservice "github.com/spacetrek-sh/spacetrek/src/service/vm"
)

// VMRepository is the subset of vmdomain.Repository the internal handler needs.
type VMRepository interface {
	GetByName(ctx context.Context, name string) (*vmdomain.VM, error)
}

// Handler is the internal VM handler. Talks to vm.Service for actions and
// to the VM repository for direct lookups (skipping service-layer caching
// the orchestrator doesn't yet have).
type Handler struct {
	vmSvc  *vmservice.Service
	vmRepo VMRepository
}

// NewHandler builds the internal VM handler. Requires both the VM service
// (for ResumeVM / MarkActive / HasSnapshot) and the VM repository (for
// hostname → VM lookup via GetByName).
func NewHandler(vmSvc *vmservice.Service, vmRepo VMRepository) *Handler {
	return &Handler{vmSvc: vmSvc, vmRepo: vmRepo}
}

// RegisterRoutes mounts the internal VM routes on the given router.
// Routes are mounted under /vm so the full paths become
// /internal/v1/vm/...
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/vm", func(r chi.Router) {
		r.Get("/by-name/{name}", h.ByName)
		r.Post("/{id}/resume", h.Resume)
		r.Post("/{id}/touch", h.Touch)
	})
}

// VMResponse is the payload returned by ByName and Resume. Carries the
// fields the activator needs to route + activate.
type VMResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	IPAddress   string `json:"ip_address"`
	ServicePort int    `json:"service_port"`
	HasSnapshot bool   `json:"has_snapshot"`
}

// ByName handles GET /internal/v1/vm/by-name/{name}.
// Returns the VM with the routing-relevant fields. 404 if missing or not
// publicly exposed (service_port == 0).
func (h *Handler) ByName(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	name := chi.URLParam(r, "name")
	if name == "" {
		httputil.WriteError(w, exception.BadRequest("missing name"))
		return
	}

	vm, err := h.vmRepo.GetByName(ctx, name)
	if err != nil {
		logger.DebugContext(ctx, "internal ByName: VM not found", "name", name, "error", err)
		httputil.WriteError(w, err)
		return
	}

	// VMs without a service port are never exposed via cloudflared, so the
	// activator treats them as 404 to match cloudflared's old behavior.
	if vm.ServicePort <= 0 {
		logger.DebugContext(ctx, "internal ByName: not exposed (service_port=0)", "name", name)
		httputil.WriteError(w, exception.NotFound("vm name", name))
		return
	}

	httputil.WriteJSON(w, http.StatusOK, "ok", toResponse(ctx, vm, h.vmSvc))
}

// Resume handles POST /internal/v1/vm/{id}/resume.
// Blocks until the VM is running (or fails). The activator calls this when
// it sees an idle VM. No chatID is passed — activator wakes don't come from
// a chat turn and don't need a chat binding; the VM just needs to be running
// so traffic can be forwarded to it. The lease table is left untouched.
func (h *Handler) Resume(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing vm id"))
		return
	}

	vm, err := h.vmSvc.ResumeVM(ctx, id, "")
	if err != nil {
		logger.WarnContext(ctx, "internal Resume: ResumeVM failed", "vm_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, "running", toResponse(ctx, vm, h.vmSvc))
}

// Touch handles POST /internal/v1/vm/{id}/touch.
// Refreshes the VM's idle deadline. Called by the activator on every
// forwarded request so an actively-served VM is not reaped mid-traffic.
func (h *Handler) Touch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing vm id"))
		return
	}

	if err := h.vmSvc.MarkActive(ctx, id); err != nil {
		logger.WarnContext(ctx, "internal Touch: MarkActive failed", "vm_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	httputil.NoContent(w)
}

func toResponse(ctx context.Context, vm *vmdomain.VM, vmSvc HasSnapshotChecker) VMResponse {
	resp := VMResponse{
		ID:          vm.ID,
		Name:        vm.Name,
		Status:      string(vm.Status),
		ServicePort: vm.ServicePort,
		HasSnapshot: vmSvc.HasSnapshot(ctx, vm.ID),
	}
	if vm.IPAddress != nil {
		resp.IPAddress = strings.TrimSpace(*vm.IPAddress)
	}
	return resp
}

// HasSnapshotChecker is the subset of vmservice.Service that toResponse needs.
// Declared locally so tests can substitute a stub.
type HasSnapshotChecker interface {
	HasSnapshot(ctx context.Context, vmID string) bool
}

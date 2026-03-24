// Package vm provides HTTP handlers and DTOs for VM endpoints.
package vm

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kumori-sh/spacetrk/pkg/auth/jwt"
	"github.com/kumori-sh/spacetrk/pkg/exception"
	httputil "github.com/kumori-sh/spacetrk/pkg/http"
	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	"github.com/kumori-sh/spacetrk/src/core/domain/environment"
	vmdomain "github.com/kumori-sh/spacetrk/src/core/domain/vm"
	"github.com/kumori-sh/spacetrk/src/middleware"
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
			r.Get("/runtimes", h.ListRuntimes)
			r.Get("/runtimes/stream", h.StreamRuntimes)
			r.Get("/{id}/metrics", h.GetMetrics)
			r.Get("/{id}/metrics/history", h.GetMetricsHistory)
			r.Get("/{id}/stream", h.StreamRuntime)
			r.Get("/{id}", h.Get)
			r.Delete("/{id}", h.Stop)
			r.Delete("/{id}/destroy", h.Destroy)
			r.Post("/{id}/execute", h.ExecuteCommand)
		})
	})
}

// GetMetricsHistory handles GET /api/v1/vm/{id}/metrics/history.
func (h *Handler) GetMetricsHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing VM ID"))
		return
	}

	limit := 300
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			httputil.WriteError(w, exception.BadRequest("invalid limit"))
			return
		}
		limit = parsed
	}

	from, err := parseHistoryTime(r.URL.Query().Get("from"))
	if err != nil {
		httputil.WriteError(w, exception.BadRequest("invalid from, use unix seconds or RFC3339"))
		return
	}

	to, err := parseHistoryTime(r.URL.Query().Get("to"))
	if err != nil {
		httputil.WriteError(w, exception.BadRequest("invalid to, use unix seconds or RFC3339"))
		return
	}

	points, err := h.vmservice.GetMetricsHistory(ctx, id, from, to, limit)
	if err != nil {
		httputil.WriteError(w, err)
		return
	}

	out := make([]vmMetricsHistoryPointResponse, 0, len(points))
	for _, point := range points {
		out = append(out, vmMetricsHistoryPointResponse{
			CPUUsagePercent:      point.CPUUsagePercent,
			MemoryUsedMB:         point.MemoryUsedMB,
			MemoryLimitMB:        point.MemoryLimitMB,
			MemoryPercent:        point.MemoryPercent,
			DiskUsedMB:           point.DiskUsedMB,
			DiskLimitMB:          point.DiskLimitMB,
			DiskPercent:          point.DiskPercent,
			NetworkBytesSent:     point.NetworkBytesSent,
			NetworkBytesReceived: point.NetworkBytesReceived,
			CollectedAt:          point.CollectedAt.Unix(),
		})
	}

	httputil.WriteJSON(w, http.StatusOK, "VM metrics history", vmMetricsHistoryResponse{
		VMID:   id,
		Points: out,
	})
}

// ListRuntimes handles GET /api/v1/vm/runtimes.
// Returns all currently running runtimes with refreshed provider state.
func (h *Handler) ListRuntimes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runtimes, err := h.vmservice.ListRunningRuntimes(ctx)
	if err != nil {
		httputil.WriteError(w, err)
		return
	}

	out := make([]runtimeSnapshotResponse, 0, len(runtimes))
	for _, vm := range runtimes {
		metrics, _ := h.vmservice.GetMetrics(ctx, vm.ID)
		out = append(out, toRuntimeSnapshotResponse(vm, metrics))
	}

	httputil.WriteJSON(w, http.StatusOK, "running runtimes", out)
}

// StreamRuntime handles GET /api/v1/vm/{id}/stream with Server-Sent Events.
func (h *Handler) StreamRuntime(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing VM ID"))
		return
	}

	prepareSSE(w)
	rc := http.NewResponseController(w)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			vm, err := h.vmservice.GetRuntimeSnapshot(ctx, id)
			if err != nil {
				writeSSEEvent(w, "error", map[string]string{"error": err.Error()})
				_ = rc.Flush()
				continue
			}
			metrics, _ := h.vmservice.GetMetrics(ctx, vm.ID)
			writeSSEEvent(w, "runtime", toRuntimeSnapshotResponse(vm, metrics))
			_ = rc.Flush()
		}
	}
}

// StreamRuntimes handles GET /api/v1/vm/runtimes/stream with Server-Sent Events.
func (h *Handler) StreamRuntimes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	prepareSSE(w)
	rc := http.NewResponseController(w)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runtimes, err := h.vmservice.ListRunningRuntimes(ctx)
			if err != nil {
				writeSSEEvent(w, "error", map[string]string{"error": err.Error()})
				_ = rc.Flush()
				continue
			}

			out := make([]runtimeSnapshotResponse, 0, len(runtimes))
			for _, vm := range runtimes {
				metrics, _ := h.vmservice.GetMetrics(ctx, vm.ID)
				out = append(out, toRuntimeSnapshotResponse(vm, metrics))
			}

			writeSSEEvent(w, "runtimes", out)
			_ = rc.Flush()
		}
	}
}

// GetMetrics handles GET /api/v1/vm/{id}/metrics.
func (h *Handler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing VM ID"))
		return
	}

	metrics, err := h.vmservice.GetMetrics(ctx, id)
	if err != nil {
		httputil.WriteError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, "VM metrics", vmMetricsResponse{
		VMID:                 id,
		CPUUsagePercent:      metrics.CPUUsagePercent,
		MemoryUsedMB:         metrics.MemoryUsedMB,
		MemoryLimitMB:        metrics.MemoryLimitMB,
		MemoryPercent:        metrics.MemoryPercent,
		DiskUsedMB:           metrics.DiskUsedMB,
		DiskLimitMB:          metrics.DiskLimitMB,
		DiskPercent:          metrics.DiskPercent,
		NetworkBytesSent:     metrics.NetworkBytesSent,
		NetworkBytesReceived: metrics.NetworkBytesReceived,
		CollectedAt:          metrics.CollectedAt,
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
		ID:              vm.ID,
		EnvironmentID:   vm.EnvironmentID,
		Provider:        string(vm.Provider),
		Status:          string(vm.Status),
		RuntimeID:       vm.RuntimeID,
		RuntimeState:    vm.RuntimeState,
		PID:             vm.PID,
		LastHeartbeatAt: formatTimePtr(vm.LastHeartbeatAt),
		IdleDeadlineAt:  formatTimePtr(vm.IdleDeadlineAt),
		VCPU:            vm.GetVCPU(env.GetVCPU()),
		MemoryMB:        vm.GetMemoryMB(env.GetMemoryMB()),
		DiskMB:          vm.GetDiskMB(env.GetDiskMB()),
	}
}

// toGetVMResponse converts a domain VM to getVMResponse.
func toGetVMResponse(vm *vmdomain.VM) getVMResponse {
	return getVMResponse{
		ID:              vm.ID,
		EnvironmentID:   vm.EnvironmentID,
		Provider:        string(vm.Provider),
		Status:          string(vm.Status),
		RuntimeID:       vm.RuntimeID,
		RuntimeState:    vm.RuntimeState,
		PID:             vm.PID,
		LastHeartbeatAt: formatTimePtr(vm.LastHeartbeatAt),
		IdleDeadlineAt:  formatTimePtr(vm.IdleDeadlineAt),
		VCPU:            vm.VCPU,
		MemoryMB:        vm.MemoryMB,
		DiskMB:          vm.DiskMB,
		HasOverrides:    vm.HasCustomResources(),
		IPAddress:       vm.IPAddress,
		ChatID:          vm.ChatID,
		CreatedAt:       vm.CreatedAt.Format("2006-01-02T15:04:05Z"),
		TerminatedAt:    formatTimePtr(vm.TerminatedAt),
		AssignedAt:      formatTimePtr(vm.AssignedAt),
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

func toRuntimeSnapshotResponse(vm *vmdomain.VM, metrics vmdomain.Metrics) runtimeSnapshotResponse {
	return runtimeSnapshotResponse{
		ID:                   vm.ID,
		EnvironmentID:        vm.EnvironmentID,
		Provider:             string(vm.Provider),
		Status:               string(vm.Status),
		RuntimeID:            vm.RuntimeID,
		RuntimeState:         vm.RuntimeState,
		PID:                  vm.PID,
		LastHeartbeatAt:      formatTimePtr(vm.LastHeartbeatAt),
		IdleDeadlineAt:       formatTimePtr(vm.IdleDeadlineAt),
		ChatID:               vm.ChatID,
		CPUUsagePercent:      metrics.CPUUsagePercent,
		MemoryUsedMB:         metrics.MemoryUsedMB,
		MemoryLimitMB:        metrics.MemoryLimitMB,
		MemoryPercent:        metrics.MemoryPercent,
		DiskUsedMB:           metrics.DiskUsedMB,
		DiskLimitMB:          metrics.DiskLimitMB,
		NetworkBytesSent:     metrics.NetworkBytesSent,
		NetworkBytesReceived: metrics.NetworkBytesReceived,
		CollectedAt:          metrics.CollectedAt,
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

func parseHistoryTime(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}

	if sec, err := strconv.ParseInt(raw, 10, 64); err == nil {
		t := time.Unix(sec, 0).UTC()
		return &t, nil
	}

	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, err
	}
	t = t.UTC()
	return &t, nil
}

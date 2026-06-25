// Package vm provides HTTP handlers and DTOs for VM endpoints.
package vm

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/spacetrek-sh/spacetrek/pkg/auth/jwt"
	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	httputil "github.com/spacetrek-sh/spacetrek/pkg/http"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	orchdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/orchestrator"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/environment"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
	"github.com/spacetrek-sh/spacetrek/src/middleware"
	vmservice "github.com/spacetrek-sh/spacetrek/src/service/vm"
)

// Handler groups all VM-related HTTP handlers.
type Handler struct {
	vmservice  *vmservice.Service
	jwtManager *jwt.Manager
	envRepo    EnvironmentRepository
	runtimes   RuntimeEventRepository
}

// RuntimeEventRepository defines the interface for querying runtime events.
type RuntimeEventRepository interface {
	ListRecent(ctx context.Context, limit int) ([]*orchdomain.PersistedRuntimeEvent, error)
}

// EnvironmentRepository defines the interface for fetching environment details.
type EnvironmentRepository interface {
	GetByID(ctx context.Context, id string) (*environment.Environment, error)
}

// NewHandler creates a new VM handler.
func NewHandler(vmSvc *vmservice.Service, jwtMgr *jwt.Manager, envRepo EnvironmentRepository, runtimes RuntimeEventRepository) *Handler {
	return &Handler{
		vmservice:  vmSvc,
		jwtManager: jwtMgr,
		envRepo:    envRepo,
		runtimes:   runtimes,
	}
}

// RegisterRoutes registers all VM routes under the given router.
// All VM routes require admin role.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Route("/vm", func(r chi.Router) {
		// Authenticated routes (any role)
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticate(h.jwtManager))
			r.Get("/fleet/stream", h.StreamFleet)
			r.Get("/activity/stream", h.StreamActivity)
		})

		// Admin-only routes
		r.Group(func(r chi.Router) {
			r.Use(middleware.Authenticate(h.jwtManager))
			r.Use(middleware.RequireRole("admin"))
			r.Post("/", h.Create)
			r.Get("/leases", h.ListLeases)
			r.Get("/runtimes", h.ListRuntimes)
			r.Get("/runtimes/stream", h.StreamRuntimes)
			r.Get("/{id}/metrics", h.GetMetrics)
			r.Get("/{id}/metrics/history", h.GetMetricsHistory)
			r.Get("/{id}/stream", h.StreamRuntime)
			r.Post("/{id}/assign", h.Assign)
			r.Post("/{id}/unassign", h.Unassign)
			// Deprecated soon for ownership lookups: prefer /vm/leases?chat_id=... for lease-aware ownership state.
			r.Get("/{id}", h.Get)
			r.Delete("/{id}", h.Stop)
			r.Delete("/{id}/destroy", h.Destroy)
			r.Post("/{id}/execute", h.ExecuteCommand)
			r.Post("/{id}/snapshot", h.CreateSnapshot)
			r.Post("/resume", h.ResumeVM)
		})
	})
}

// ListLeases handles GET /api/v1/vm/leases?chat_id=... and returns active leases for a chat.
func (h *Handler) ListLeases(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	chatID := r.URL.Query().Get("chat_id")
	if chatID == "" {
		httputil.WriteError(w, exception.BadRequest("missing chat_id"))
		return
	}

	logger.DebugContext(ctx, "list VM leases requested", "chat_id", chatID)

	leases, err := h.vmservice.ListActiveLeasesByChat(ctx, chatID)
	if err != nil {
		logger.WarnContext(ctx, "list VM leases failed", "chat_id", chatID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.DebugContext(ctx, "list VM leases result", "chat_id", chatID, "count", len(leases))

	out := make([]vmLeaseResponse, 0, len(leases))
	for _, lease := range leases {
		out = append(out, vmLeaseResponse{
			ID:       lease.ID,
			ChatID:   lease.ChatID,
			VMID:     lease.VMID,
			LeasedAt: lease.LeasedAt.UTC().Format(time.RFC3339),
		})
	}

	httputil.WriteJSON(w, http.StatusOK, "active VM leases", out)
}

// Assign handles POST /api/v1/vm/{id}/assign.
func (h *Handler) Assign(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing VM ID"))
		return
	}

	var req assignVMRequest
	if err := httputil.BindJSON(r, &req); err != nil {
		logger.WarnContext(ctx, "VM assign failed", "vm_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.DebugContext(ctx, "VM assign requested", "vm_id", id, "chat_id", req.ChatID)

	vm, err := h.vmservice.AssignToChat(ctx, id, req.ChatID)
	if err != nil {
		logger.WarnContext(ctx, "VM assign failed", "vm_id", id, "chat_id", req.ChatID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, "VM assigned", toGetVMResponse(vm))
}

// Unassign handles POST /api/v1/vm/{id}/unassign.
func (h *Handler) Unassign(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing VM ID"))
		return
	}

	logger.DebugContext(ctx, "VM unassign requested", "vm_id", id)

	vm, err := h.vmservice.Unassign(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "VM unassign failed", "vm_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, "VM unassigned", toGetVMResponse(vm))
}

// GetMetricsHistory handles GET /api/v1/vm/{id}/metrics/history.
func (h *Handler) GetMetricsHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

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
		logger.WarnContext(ctx, "VM metrics history failed", "vm_id", id, "error", err)
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
	logger := pkglog.FromContext(ctx)

	logger.DebugContext(ctx, "list running runtimes requested")

	runtimes, err := h.vmservice.ListRunningRuntimes(ctx)
	if err != nil {
		logger.WarnContext(ctx, "list running runtimes failed", "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.DebugContext(ctx, "list running runtimes result", "count", len(runtimes))

	out := make([]runtimeSnapshotResponse, 0, len(runtimes))
	for _, vm := range runtimes {
		metrics, _ := h.vmservice.GetCachedMetrics(vm.ID)
		out = append(out, toRuntimeSnapshotResponse(vm, metrics))
	}

	httputil.WriteJSON(w, http.StatusOK, "running runtimes", out)
}

// StreamRuntime handles GET /api/v1/vm/{id}/stream with Server-Sent Events.
func (h *Handler) StreamRuntime(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing VM ID"))
		return
	}

	logger.DebugContext(ctx, "VM runtime stream opened", "vm_id", id)
	httputil.PrepareSSE(w)
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if err := httputil.WriteSSEHeartbeat(w); err != nil {
				return
			}
			_ = rc.Flush()
		case <-ticker.C:
			vm, err := h.vmservice.GetRuntimeSnapshot(ctx, id)
			if err != nil {
				if writeErr := httputil.WriteSSEEvent(w, "error", map[string]string{"error": err.Error()}); writeErr != nil {
					return
				}
				_ = rc.Flush()
				continue
			}
			metrics, _ := h.vmservice.GetCachedMetrics(vm.ID)
			if err := httputil.WriteSSEEvent(w, "runtime", toRuntimeSnapshotResponse(vm, metrics)); err != nil {
				return
			}
			_ = rc.Flush()
		}
	}
}

// StreamRuntimes handles GET /api/v1/vm/runtimes/stream with Server-Sent Events.
func (h *Handler) StreamRuntimes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	logger.DebugContext(ctx, "VM runtimes stream opened")
	httputil.PrepareSSE(w)
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if err := httputil.WriteSSEHeartbeat(w); err != nil {
				return
			}
			_ = rc.Flush()
		case <-ticker.C:
			entries, err := h.vmservice.ListCachedFleetSnapshot(ctx, "", "admin")
			if err != nil {
				if writeErr := httputil.WriteSSEEvent(w, "error", map[string]string{"error": err.Error()}); writeErr != nil {
					return
				}
				_ = rc.Flush()
				continue
			}

			out := make([]runtimeSnapshotResponse, 0, len(entries))
			for _, entry := range entries {
				out = append(out, toRuntimeSnapshotResponse(entry.VM, entry.Metrics))
			}

			if err := httputil.WriteSSEEvent(w, "runtimes", out); err != nil {
				return
			}
			_ = rc.Flush()
		}
	}
}

// StreamFleet handles GET /api/v1/vm/fleet/stream with Server-Sent Events.
// Emits frontend-friendly fleet snapshots every 2 seconds.
// Admin users see all VMs; regular users see only their own.
func (h *Handler) StreamFleet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	userID := middleware.GetUserID(ctx)
	role := middleware.GetUserRole(ctx)
	logger.DebugContext(ctx, "VM fleet stream opened", "user_id", userID, "role", role)

	httputil.PrepareSSE(w)
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.DebugContext(ctx, "VM fleet stream closed")
			return
		case <-heartbeat.C:
			if err := httputil.WriteSSEHeartbeat(w); err != nil {
				return
			}
			_ = rc.Flush()
		case <-ticker.C:
			entries, err := h.vmservice.ListCachedFleetSnapshot(ctx, userID, role)
			if err != nil {
				logger.WarnContext(ctx, "fleet stream: failed to list runtimes", "error", err)
				if writeErr := httputil.WriteSSEEvent(w, "error", map[string]string{"error": err.Error()}); writeErr != nil {
					return
				}
				_ = rc.Flush()
				continue
			}

			out := make([]fleetVMResponse, 0, len(entries))
			for _, entry := range entries {
				out = append(out, toFleetVMResponse(entry.VM, entry.Metrics))
			}

			if err := httputil.WriteSSEEvent(w, "fleet", out); err != nil {
				return
			}
			_ = rc.Flush()
		}
	}
}

// StreamActivity handles GET /api/v1/vm/activity/stream with Server-Sent Events.
// Emits recent runtime events every 2 seconds, tracking the last-seen timestamp
// to only push new events. Admin sees all events; regular users see only their own.
func (h *Handler) StreamActivity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	userID := middleware.GetUserID(ctx)
	role := middleware.GetUserRole(ctx)
	logger.DebugContext(ctx, "VM activity stream opened", "user_id", userID, "role", role)

	httputil.PrepareSSE(w)
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	var lastSeen time.Time

	for {
		select {
		case <-ctx.Done():
			logger.DebugContext(ctx, "VM activity stream closed")
			return
		case <-heartbeat.C:
			if err := httputil.WriteSSEHeartbeat(w); err != nil {
				return
			}
			_ = rc.Flush()
		case <-ticker.C:
			if h.runtimes == nil {
				continue
			}

			events, err := h.runtimes.ListRecent(ctx, 100)
			if err != nil {
				logger.WarnContext(ctx, "activity stream: failed to list recent events", "error", err)
				if writeErr := httputil.WriteSSEEvent(w, "error", map[string]string{"error": err.Error()}); writeErr != nil {
					return
				}
				_ = rc.Flush()
				continue
			}

			var newEvents []activityEventResponse
			for i := len(events) - 1; i >= 0; i-- {
				e := events[i]
				if !e.CreatedAt.After(lastSeen) {
					continue
				}
				newEvents = append(newEvents, toActivityEvent(e))
			}

			if len(newEvents) > 0 {
				lastSeen = events[0].CreatedAt
				for i, j := 0, len(newEvents)-1; i < j; i, j = i+1, j-1 {
					newEvents[i], newEvents[j] = newEvents[j], newEvents[i]
				}
				if err := httputil.WriteSSEEvent(w, "activity", newEvents); err != nil {
					return
				}
				_ = rc.Flush()
			}
		}
	}
}

// GetMetrics handles GET /api/v1/vm/{id}/metrics.
func (h *Handler) GetMetrics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing VM ID"))
		return
	}

	logger.DebugContext(ctx, "VM metrics requested", "vm_id", id)

	metrics, err := h.vmservice.GetMetrics(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "VM metrics retrieval failed", "vm_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.DebugContext(ctx, "VM metrics result", "vm_id", id, "cpu_percent", metrics.CPUUsagePercent, "mem_percent", metrics.MemoryPercent)

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

	logger.DebugContext(ctx, "VM create requested", "env_id", req.EnvironmentID, "conversation_id", req.ConversationID, "provider", req.Provider, "name", req.Name, "workspace_size_gb", req.WorkspaceSizeGB, "vcpu", req.VCPU, "memory_mb", req.MemoryMB, "disk_mb", req.DiskMB)

	// Parse provider
	var provider vmdomain.Provider
	if req.Provider != "" {
		provider = vmdomain.Provider(req.Provider)
	}

	vm, err := h.vmservice.Create(ctx, req.EnvironmentID, req.ConversationID, provider, req.Name, req.WorkspaceSizeGB, req.VCPU, req.MemoryMB, req.DiskMB)
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

	logger.InfoContext(ctx, "VM created", "vm_id", vm.ID, "vm_name", vm.Name, "env_id", req.EnvironmentID)
	httputil.Created(w, "VM created", toCreateVMResponse(vm, env))
}

// Get handles GET /api/v1/vm/{id}. Retrieves details of the specified VM.
// Deprecated soon for lease ownership reads: prefer GET /api/v1/vm/leases?chat_id=... .
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing VM ID"))
		return
	}

	logger.DebugContext(ctx, "VM get requested", "vm_id", id)

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

	logger.DebugContext(ctx, "VM stop requested", "vm_id", id)

	vm, err := h.vmservice.Stop(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "VM stop failed", "vm_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "VM stopped", "vm_id", id, "vm_name", vm.Name)
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

	logger.DebugContext(ctx, "VM destroy requested", "vm_id", id)

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

	logger.DebugContext(ctx, "execute command request received", "vm_id", id, "command_len", len(req.Command), "command_preview", logPreview(req.Command, 256))

	output, err := h.vmservice.ExecuteCommand(ctx, id, req.Command)
	if err != nil {
		logger.WarnContext(ctx, "command execution failed", "vm_id", id, "command_preview", logPreview(req.Command, 256), "error", err)
		if output != "" {
			httputil.WriteJSON(w, http.StatusOK, "command executed", executeCommandResponse{Output: output, Error: err.Error()})
		} else {
			httputil.WriteError(w, err)
		}
		return
	}

	logger.DebugContext(ctx, "execute command result", "vm_id", id, "output_len", len(output), "output_preview", logPreview(output, 256))
	logger.InfoContext(ctx, "command executed", "vm_id", id, "command", req.Command)
	httputil.WriteJSON(w, http.StatusOK, "command executed", executeCommandResponse{Output: output})
}

// CreateSnapshot handles POST /api/v1/vm/{id}/snapshot. Creates a snapshot of the VM.
func (h *Handler) CreateSnapshot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	id := chi.URLParam(r, "id")
	if id == "" {
		httputil.WriteError(w, exception.BadRequest("missing VM ID"))
		return
	}

	logger.DebugContext(ctx, "VM snapshot requested", "vm_id", id)

	snap, err := h.vmservice.CreateSnapshot(ctx, id)
	if err != nil {
		logger.WarnContext(ctx, "VM snapshot failed", "vm_id", id, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "VM snapshot created", "vm_id", id, "snapshot_id", snap.ID)
	httputil.Created(w, "snapshot created", vmSnapshotResponse{
		ID:        snap.ID,
		VMID:      snap.VMID,
		Type:      string(snap.Type),
		SizeBytes: snap.SizeBytes,
		CreatedAt: snap.CreatedAt.Format("2006-01-02T15:04:05Z"),
	})
}

// ResumeVM handles POST /api/v1/vm/resume. Resumes a VM from snapshot for a chat.
func (h *Handler) ResumeVM(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	logger := pkglog.FromContext(ctx)

	var req resumeVMRequest
	if err := httputil.BindJSON(r, &req); err != nil {
		logger.WarnContext(ctx, "VM resume failed", "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.DebugContext(ctx, "VM resume requested", "chat_id", req.ChatID)

	vm, err := h.vmservice.FindPreviousLeaseForChat(ctx, req.ChatID)
	if err != nil {
		logger.WarnContext(ctx, "VM resume: no previous VM found", "chat_id", req.ChatID, "error", err)
		httputil.WriteError(w, exception.NotFound("previous vm for chat", req.ChatID))
		return
	}

	resumed, err := h.vmservice.ResumeVM(ctx, vm.ID, req.ChatID)
	if err != nil {
		logger.WarnContext(ctx, "VM resume failed", "vm_id", vm.ID, "chat_id", req.ChatID, "error", err)
		httputil.WriteError(w, err)
		return
	}

	logger.InfoContext(ctx, "VM resumed", "vm_id", resumed.ID, "chat_id", req.ChatID)
	httputil.WriteJSON(w, http.StatusOK, "VM resumed", toGetVMResponse(resumed))
}

// toCreateVMResponse converts a domain VM to createVMResponse.
func toCreateVMResponse(vm *vmdomain.VM, env *environment.Environment) createVMResponse {
	return createVMResponse{
		ID:              vm.ID,
		Name:            vm.Name,
		EnvironmentID:   vm.EnvironmentID,
		ConversationID:  vm.ConversationID,
		Provider:        string(vm.Provider),
		Status:          string(vm.Status),
		WorkspaceSizeGB: vm.WorkspaceSizeGB,
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
		Name:            vm.Name,
		EnvironmentID:   vm.EnvironmentID,
		ConversationID:  vm.ConversationID,
		Provider:        string(vm.Provider),
		Status:          string(vm.Status),
		WorkspaceSizeGB: vm.WorkspaceSizeGB,
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

func logPreview(text string, limit int) string {
	normalized := strings.TrimSpace(text)
	if normalized == "" {
		return ""
	}

	normalized = strings.ReplaceAll(normalized, "\r", "\\r")
	normalized = strings.ReplaceAll(normalized, "\n", "\\n")

	if limit <= 0 || len(normalized) <= limit {
		return normalized
	}

	return normalized[:limit] + "...(truncated)"
}

func toRuntimeSnapshotResponse(vm *vmdomain.VM, metrics vmdomain.Metrics) runtimeSnapshotResponse {
	return runtimeSnapshotResponse{
		ID:                   vm.ID,
		Name:                 vm.Name,
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

func toFleetVMResponse(vm *vmdomain.VM, metrics vmdomain.Metrics) fleetVMResponse {
	ip := ""
	if vm.IPAddress != nil {
		ip = *vm.IPAddress
	}
	return fleetVMResponse{
		ID:      vm.ID,
		Name:    vm.Name,
		Uptime:  formatDuration(time.Since(vm.CreatedAt)),
		Mem:     fmt.Sprintf("%d / %dmb", metrics.MemoryUsedMB, metrics.MemoryLimitMB),
		MemPct:  metrics.MemoryPercent,
		CPU:     fmt.Sprintf("%.0f%%", metrics.CPUUsagePercent),
		Disk:    fmt.Sprintf("%dmb / %dmb", metrics.DiskUsedMB, metrics.DiskLimitMB),
		DiskPct: metrics.DiskPercent,
		Status:  string(vm.Status),
		IP:      ip,
		Created: vm.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func toActivityEvent(e *orchdomain.PersistedRuntimeEvent) activityEventResponse {
	vmID := ""
	vmName := ""
	if e.Metadata != nil {
		if v, ok := e.Metadata["vm_id"].(string); ok {
			vmID = v
		}
		if v, ok := e.Metadata["vm_name"].(string); ok {
			vmName = v
		}
	}

	msg := e.Data
	if e.Command != "" {
		msg = e.Command
	}
	if e.Error != "" {
		msg = e.Error
	}

	// Prefer the human-readable name; fall back to the UUID if the event
	// predates vm_name enrichment. Until the orchestrator stamps vm_name
	// into metadata at all emit sites this is a graceful degradation.
	vmDisplay := vmName
	if vmDisplay == "" {
		vmDisplay = vmID
	}

	return activityEventResponse{
		Time: e.CreatedAt.Format("15:04:05"),
		Type: mapActivityType(e.Type, e.Data),
		VM:   vmDisplay,
		Msg:  msg,
	}
}

func mapActivityType(t orchdomain.RuntimeEventType, data string) string {
	switch t {
	case orchdomain.EventToolCall:
		switch data {
		case "vm.write_file", "vm.edit_file":
			return "write"
		case "vm.create", "vm.start":
			return "boot"
		default:
			return "exec"
		}
	case orchdomain.EventError:
		return "error"
	default:
		return string(t)
	}
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) - m*60
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := int(d.Hours())
	m = m - h*60
	return fmt.Sprintf("%dh %dm", h, m)
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

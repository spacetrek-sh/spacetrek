package activator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
)

// Server is the activator's HTTP handler. It resolves the incoming Host
// header to a VM, activates it if idle, and forwards the request.
//
// In ModeCloudflared (default), requests arrive from cloudflared with the
// path stripped to root; the target hostname is read from the Host header.
//
// In ModeMeshProxy, requests arrive from an in-mesh HTTP client that has
// HTTP_PROXY pointed at this server. The client emits absolute-form
// request lines ("GET http://host/path HTTP/1.1"), so the target hostname
// is read from r.URL.Host instead.
type Server struct {
	mode         Mode
	domainSuffix string
	orch         *OrchestratorClient
	activator    *Activator
	logger       *slog.Logger
	coldStart    time.Duration
}

// Mode selects how the activator extracts the target hostname.
type Mode string

const (
	// ModeCloudflared is the public-ingress mode: traffic from cloudflared,
	// hostname in Host header.
	ModeCloudflared Mode = "cloudflared"
	// ModeMeshProxy is the in-mesh forward-proxy mode: traffic from VMs
	// using HTTP_PROXY, hostname in URL.Host.
	ModeMeshProxy Mode = "mesh-proxy"
)

// Config configures the activator server.
type Config struct {
	Mode                Mode          // ModeCloudflared (default) or ModeMeshProxy
	DomainSuffix        string        // e.g. ".box.spacetrek.xyz"
	OrchestratorClient  *OrchestratorClient
	Activator           *Activator
	Logger              *slog.Logger
	ColdStartBudget     time.Duration
}

// NewServer constructs the activator handler.
func NewServer(cfg Config) *Server {
	if cfg.ColdStartBudget <= 0 {
		cfg.ColdStartBudget = 60 * time.Second
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeCloudflared
	}
	return &Server{
		mode:         cfg.Mode,
		domainSuffix: cfg.DomainSuffix,
		orch:         cfg.OrchestratorClient,
		activator:    cfg.Activator,
		logger:       cfg.Logger,
		coldStart:    cfg.ColdStartBudget,
	}
}

// ServeHTTP implements http.Handler. /healthz short-circuits; all other
// paths go through the activate-and-forward pipeline.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}

	ctx := r.Context()
	logger := pkglog.FromContext(ctx).With("host", r.Host, "path", r.URL.Path)

	vmName, ok := s.resolveVMName(r)
	if !ok {
		logger.DebugContext(ctx, "activator: host suffix mismatch")
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	logger = logger.With("vm_name", vmName)

	vm, exists, err := s.orch.LookupVM(ctx, vmName)
	if err != nil {
		logger.WarnContext(ctx, "activator: lookup failed", "error", err)
		http.Error(w, "lookup failed", http.StatusBadGateway)
		return
	}
	if !exists || vm == nil {
		logger.DebugContext(ctx, "activator: vm not exposed")
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	logger = logger.With("vm_id", vm.ID, "status", vm.Status)

	switch vm.Status {
	case "running":
		// Already warm — forward directly.
		s.forward(w, r, vm, logger)
	case "idle":
		s.activateAndForward(ctx, w, r, vm, logger)
	case "terminated":
		http.Error(w, "vm terminated", http.StatusGone)
	case "ready", "provisioning", "creating":
		// VM is mid-transition. Brief wait then 503; client retries.
		w.Header().Set("Retry-After", "5")
		http.Error(w, "vm not ready, retry", http.StatusServiceUnavailable)
	default:
		w.Header().Set("Retry-After", "5")
		http.Error(w, "vm in unexpected state: "+vm.Status, http.StatusServiceUnavailable)
	}
}

// activateAndForward runs the cold-start under the budget, then forwards.
func (s *Server) activateAndForward(ctx context.Context, w http.ResponseWriter, r *http.Request, vm *VMInfo, logger *slog.Logger) {
	if !vm.HasSnapshot {
		logger.WarnContext(ctx, "activator: idle vm has no snapshot")
		http.Error(w, "vm has no snapshot to restore", http.StatusGone)
		return
	}

	coldCtx, cancel := context.WithTimeout(ctx, s.coldStart)
	defer cancel()

	logger.InfoContext(coldCtx, "activator: cold-start begin")
	start := time.Now()
	resumed, err := s.activator.Activate(coldCtx, vm.ID)
	if err != nil {
		if errors.Is(err, ErrActivationsFull) {
			logger.WarnContext(coldCtx, "activator: activation capacity exhausted")
			w.Header().Set("Retry-After", "5")
			http.Error(w, "activation capacity exhausted", http.StatusServiceUnavailable)
			return
		}
		logger.ErrorContext(coldCtx, "activator: activation failed", "error", err)
		http.Error(w, "activation failed", http.StatusBadGateway)
		return
	}
	logger.InfoContext(coldCtx, "activator: cold-start complete", "duration_ms", time.Since(start).Milliseconds())
	s.forward(w, r, resumed, logger)
}

// forward proxies the request to vm.IPAddress:vm.ServicePort and refreshes
// the VM's idle deadline on success.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, vm *VMInfo, logger *slog.Logger) {
	target := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("%s:%d", vm.IPAddress, vm.ServicePort),
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.WarnContext(r.Context(), "activator: forward failed",
			"target", target.Host, "error", err)
		http.Error(w, "backend unreachable", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)

	// Fire-and-forget touch so the idle reaper doesn't reclaim an
	// actively-served VM.
	go func() {
		touchCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.orch.Touch(touchCtx, vm.ID)
	}()
}

// resolveVMName extracts the VM hostname from the request and strips the
// domain suffix. In ModeCloudflared the hostname comes from r.Host; in
// ModeMeshProxy it comes from r.URL.Host (absolute-form request line).
// Returns ok=false if the host doesn't match the configured suffix —
// callers treat that as a 404 so the activator refuses to act as an
// open proxy for non-mesh destinations.
func (s *Server) resolveVMName(r *http.Request) (string, bool) {
	host := r.Host
	if s.mode == ModeMeshProxy {
		host = r.URL.Host
	}
	if host == "" {
		return "", false
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i] // strip :port
	}
	if !strings.HasSuffix(host, s.domainSuffix) {
		return "", false
	}
	name := strings.TrimSuffix(host, s.domainSuffix)
	if name == "" {
		return "", false
	}
	return name, true
}

// Compile-time check that Server satisfies http.Handler.
var _ http.Handler = (*Server)(nil)

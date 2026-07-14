// Package tunnelwriter renders the cloudflared ingress config that routes
// public *.box.spacetrek.xyz traffic to the spacetrek-activator container.
//
// All VM traffic is collapsed into a single wildcard rule pointing at the
// activator's listener (http://<orchestrator-eth0-ip>:8090). The activator
// then resolves the VM by hostname, wakes it if idle, and forwards to
// vmIP:service_port. Per-VM ingress rules are no longer needed — the
// activator reads VM state from the orchestrator's internal API on every
// request.
//
// cloudflared does not hot-reload a file-based config; the cloudflared
// container's entrypoint watches this file via inotifywait and re-execs
// cloudflared when it changes. Triggered on VM lifecycle transitions (so
// the file is rewrited at least once after boot) and by a periodic
// reconciler that catches external edits to the static-rules file.
package tunnelwriter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
)

// Writer renders the cloudflared ingress config. The rendered file is
// stable given fixed inputs — it does not depend on VM state.
type Writer struct {
	ingressPath     string
	domainSuffix    string // e.g. ".box.spacetrek.xyz"
	orchestratorIP  string // e.g. "172.19.0.4" — orchestrator's eth0 IP
	header          string // static preamble (tunnel:, credentials-file:, etc.)
	staticRulesPath string // optional file with extra ingress entries (non-VM services)

	mu       sync.Mutex
	lastHash string
}

// New returns a Writer that writes to ingressPath, producing hostnames of
// the form *<domainSuffix> routed at http://<orchestratorIP>:8090 (the
// activator). header is written verbatim before the ingress: block — it
// carries the tunnel UUID and credentials-file directives that cloudflared
// requires. staticRulesPath, when non-empty and pointing at a readable
// file, supplies verbatim YAML entries inserted between `ingress:` and the
// wildcard VM rule — used for non-VM services (api, www) that share the
// tunnel. Missing file is treated as empty (no static rules); other read
// errors fail the Refresh.
func New(ingressPath, domainSuffix, orchestratorIP, header, staticRulesPath string) *Writer {
	return &Writer{
		ingressPath:     ingressPath,
		domainSuffix:    domainSuffix,
		orchestratorIP:  orchestratorIP,
		header:          header,
		staticRulesPath: staticRulesPath,
	}
}

// Refresh rewrites the ingress file if the rendered content changed since
// the last successful write. No-op if unchanged. Safe to call repeatedly.
//
// Refresh does not signal cloudflared — the host's systemd .path unit
// watches the file and reload-or-restarts cloudflared on change.
func (w *Writer) Refresh(ctx context.Context) error {
	logger := pkglog.FromContext(ctx)

	static, err := w.readStaticRules()
	if err != nil {
		return fmt.Errorf("read static rules: %w", err)
	}

	content := render(w.domainSuffix, w.orchestratorIP, w.header, static)
	hash := hashContent(content)

	w.mu.Lock()
	if hash == w.lastHash {
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()

	if err := w.writeAtomic(content); err != nil {
		return fmt.Errorf("write ingress file: %w", err)
	}

	w.mu.Lock()
	w.lastHash = hash
	w.mu.Unlock()

	logger.InfoContext(ctx, "cloudflared ingress rewritten",
		"path", w.ingressPath, "target", w.orchestratorIP)
	return nil
}

// StartReconciler runs Refresh on a ticker until ctx is cancelled. The
// first tick is immediate (matches the background-worker pattern in
// hostswriter).
func (w *Writer) StartReconciler(ctx context.Context, interval time.Duration) {
	logger := pkglog.FromContext(ctx)
	if interval <= 0 {
		interval = 60 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.InfoContext(ctx, "cloudflared ingress reconciler started",
		"interval", interval.String(), "path", w.ingressPath)
	if err := w.Refresh(ctx); err != nil {
		logger.WarnContext(ctx, "cloudflared ingress initial refresh failed", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			logger.InfoContext(ctx, "cloudflared ingress reconciler stopped")
			return
		case <-ticker.C:
			if err := w.Refresh(ctx); err != nil {
				logger.WarnContext(ctx, "cloudflared ingress refresh failed", "error", err)
			}
		}
	}
}

// render produces the ingress YAML. Pure function for golden-file testing.
// Format:
//
//	# managed by spacetrk orchestrator — do not edit
//	<header>
//	ingress:
//	  <static rules, if any>
//	  - hostname: *<suffix>
//	    service: http://<orchestratorIP>:8090
//	  - service: http_status:404
//
// The trailing http_status:404 is required by cloudflared — every ingress
// ruleset must terminate in a catch-all. header carries the static tunnel:
// and credentials-file: directives. staticRules is inserted verbatim
// between `ingress:` and the wildcard rule when non-empty.
func render(domainSuffix, orchestratorIP, header, staticRules string) []byte {
	var buf bytes.Buffer
	buf.WriteString("# managed by spacetrk orchestrator — do not edit\n")
	if header != "" {
		buf.WriteString(header)
		if !strings.HasSuffix(header, "\n") {
			buf.WriteString("\n")
		}
	}
	buf.WriteString("ingress:\n")
	if staticRules != "" {
		buf.WriteString(staticRules)
		if !strings.HasSuffix(staticRules, "\n") {
			buf.WriteString("\n")
		}
	}
	fmt.Fprintf(&buf, "  - hostname: \"*%s\"\n", domainSuffix)
	fmt.Fprintf(&buf, "    service: http://%s:8090\n", orchestratorIP)
	buf.WriteString("  - service: http_status:404\n")
	return buf.Bytes()
}

// readStaticRules reads the static-rules file. Missing file → empty string
// (no static rules). Other read errors propagate.
func (w *Writer) readStaticRules() (string, error) {
	if w.staticRulesPath == "" {
		return "", nil
	}
	b, err := os.ReadFile(w.staticRulesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}

func (w *Writer) writeAtomic(content []byte) error {
	dir := filepath.Dir(w.ingressPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".cloudflared-ingress.*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op if rename succeeds

	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, w.ingressPath); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}

func hashContent(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

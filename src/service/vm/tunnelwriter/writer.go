// Package tunnelwriter renders a cloudflared ingress config from VM state.
// cloudflared does not hot-reload a file-based config; the cloudflared
// container's entrypoint watches this file via inotifywait and re-execs
// cloudflared when it changes. Triggered on VM lifecycle transitions and
// by a periodic reconciler that catches missed events.
package tunnelwriter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// VMReader is the subset of vmdomain.Repository the writer needs. Declared
// locally so tests can substitute a stub without spinning up a full repo.
type VMReader interface {
	List(ctx context.Context) ([]*vmdomain.VM, error)
}

// Writer renders the cloudflared ingress config from VM state.
type Writer struct {
	reader          VMReader
	ingressPath     string
	domainSuffix    string // e.g. ".box.spacetrek.xyz"
	header          string // static preamble (tunnel:, credentials-file:, etc.)
	staticRulesPath string // optional file with extra ingress entries (static services)

	mu       sync.Mutex
	lastHash string
}

// New returns a Writer that reads VMs from reader, writes to ingressPath,
// and produces hostnames of the form <vm-name><domainSuffix>. header is
// written verbatim before the ingress: block — it carries the tunnel UUID
// and credentials-file directives that cloudflared requires.
//
// staticRulesPath, when non-empty and pointing at a readable file, supplies
// verbatim YAML entries inserted between `ingress:` and the dynamic VM
// entries — used for non-VM services (api, www) that share the tunnel.
// Missing file is treated as empty (no static rules); other read errors
// fail the Refresh.
func New(reader VMReader, ingressPath, domainSuffix, header, staticRulesPath string) *Writer {
	return &Writer{
		reader:          reader,
		ingressPath:     ingressPath,
		domainSuffix:    domainSuffix,
		header:          header,
		staticRulesPath: staticRulesPath,
	}
}

// Refresh queries VMs, rewrites the ingress file if the rendered content
// changed. No-op (no write) if the rendered content matches the last
// successful write. Safe to call repeatedly.
//
// Refresh does not signal cloudflared — the host's systemd .path unit
// watches the file and reload-or-restarts cloudflared on change.
func (w *Writer) Refresh(ctx context.Context) error {
	logger := pkglog.FromContext(ctx)

	vms, err := w.reader.List(ctx)
	if err != nil {
		return fmt.Errorf("list vms: %w", err)
	}

	static, err := w.readStaticRules()
	if err != nil {
		return fmt.Errorf("read static rules: %w", err)
	}

	content := render(vms, w.domainSuffix, w.header, static)
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

	logger.InfoContext(ctx, "cloudflared ingress rewritten", "path", w.ingressPath, "vm_count", countEligible(vms))
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

// render produces the ingress YAML from a VM slice. Pure function for
// golden-file testing. Format:
//
//	# managed by spacetrk orchestrator — do not edit
//	<header>
//	ingress:
//	  <static rules, if any>
//	  - hostname: <name><suffix>
//	    service: http://<ip>:<port>
//	  - service: http_status:404
//
// The trailing http_status:404 is required by cloudflared — every ingress
// ruleset must terminate in a catch-all. header carries the static tunnel:
// and credentials-file: directives. staticRules is inserted verbatim
// between `ingress:` and the dynamic VM entries when non-empty.
func render(vms []*vmdomain.VM, domainSuffix, header, staticRules string) []byte {
	eligible := make([]*vmdomain.VM, 0, len(vms))
	for _, vm := range vms {
		if vm == nil || vm.IsTerminated() {
			continue
		}
		if vm.Name == "" {
			continue
		}
		if vm.IPAddress == nil || *vm.IPAddress == "" {
			continue
		}
		if vm.ServicePort <= 0 {
			continue
		}
		eligible = append(eligible, vm)
	}

	sort.Slice(eligible, func(i, j int) bool {
		return eligible[i].Name < eligible[j].Name
	})

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
	for _, vm := range eligible {
		fmt.Fprintf(&buf, "  - hostname: %s%s\n", vm.Name, domainSuffix)
		fmt.Fprintf(&buf, "    service: http://%s:%d\n", *vm.IPAddress, vm.ServicePort)
	}
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

func countEligible(vms []*vmdomain.VM) int {
	n := 0
	for _, vm := range vms {
		if vm == nil || vm.IsTerminated() || vm.Name == "" {
			continue
		}
		if vm.IPAddress == nil || *vm.IPAddress == "" || vm.ServicePort <= 0 {
			continue
		}
		n++
	}
	return n
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

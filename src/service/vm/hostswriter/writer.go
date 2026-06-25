// Package hostswriter renders a dnsmasq addn-hosts file from VM state and
// SIGHUPs dnsmasq to reload it. Triggered on VM lifecycle transitions and by
// a periodic reconciler that catches missed events.
package hostswriter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// VMReader is the subset of vmdomain.Repository the writer needs. Declared
// locally so tests can substitute a stub without spinning up a full repo.
type VMReader interface {
	List(ctx context.Context) ([]*vmdomain.VM, error)
}

// Writer renders the dnsmasq addn-hosts file from VM state.
type Writer struct {
	reader    VMReader
	hostsPath string

	// pidofCmd is overridable for tests; defaults to "pidof dnsmasq".
	pidofCmd []string

	// mu guards lastHash. Refresh is called from lifecycle hooks inline on
	// the service goroutine, from the reconciler ticker, and from startup —
	// they can race.
	mu       sync.Mutex
	lastHash string
}

// New returns a Writer that reads VMs from reader and writes to hostsPath.
func New(reader VMReader, hostsPath string) *Writer {
	return &Writer{
		reader:    reader,
		hostsPath: hostsPath,
		pidofCmd:  []string{"pidof", "dnsmasq"},
	}
}

// Refresh queries VMs, rewrites the hosts file if the content changed, then
// SIGHUPs dnsmasq. No-op (no write, no HUP) if the rendered content matches
// the last successful write. Safe to call repeatedly.
func (w *Writer) Refresh(ctx context.Context) error {
	logger := pkglog.FromContext(ctx)

	vms, err := w.reader.List(ctx)
	if err != nil {
		return fmt.Errorf("list vms: %w", err)
	}

	content := render(vms)
	hash := hashContent(content)

	w.mu.Lock()
	if hash == w.lastHash {
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()

	if err := w.writeAtomic(content); err != nil {
		return fmt.Errorf("write hosts file: %w", err)
	}

	w.mu.Lock()
	w.lastHash = hash
	w.mu.Unlock()

	w.hup(ctx, logger)
	return nil
}

// StartReconciler runs Refresh on a ticker until ctx is cancelled. The first
// tick is immediate (matches the existing background-worker pattern in
// service.go).
func (w *Writer) StartReconciler(ctx context.Context, interval time.Duration) {
	logger := pkglog.FromContext(ctx)
	if interval <= 0 {
		interval = 60 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	logger.InfoContext(ctx, "VM hosts-file reconciler started", "interval", interval.String(), "path", w.hostsPath)
	if err := w.Refresh(ctx); err != nil {
		logger.WarnContext(ctx, "VM hosts-file initial refresh failed", "error", err)
	}

	for {
		select {
		case <-ctx.Done():
			logger.InfoContext(ctx, "VM hosts-file reconciler stopped")
			return
		case <-ticker.C:
			if err := w.Refresh(ctx); err != nil {
				logger.WarnContext(ctx, "VM hosts-file refresh failed", "error", err)
			}
		}
	}
}

// render produces the hosts-file bytes from a VM slice. Pure function for
// golden-file testing. Format (see docs/issues/vm-dns-naming.md §Hosts File
// Format):
//
//	# managed by spacetrk orchestrator — do not edit
//	<ip>   <name>.vm.internal    <name>
//
// Two labels per line: the FQDN used by peers and the future port-forwarding
// proxy, plus the bare label used inside guests via expand-hosts.
func render(vms []*vmdomain.VM) []byte {
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
		eligible = append(eligible, vm)
	}

	sort.Slice(eligible, func(i, j int) bool {
		return eligible[i].Name < eligible[j].Name
	})

	var buf bytes.Buffer
	buf.WriteString("# managed by spacetrk orchestrator — do not edit\n")
	for _, vm := range eligible {
		fmt.Fprintf(&buf, "%s   %s.vm.internal\t%s\n", *vm.IPAddress, vm.Name, vm.Name)
	}
	return buf.Bytes()
}

func (w *Writer) writeAtomic(content []byte) error {
	dir := filepath.Dir(w.hostsPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".vm-hosts.*")
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
	if err := os.Rename(tmpName, w.hostsPath); err != nil {
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}

// hup SIGHUPs the dnsmasq process so it reloads the addn-hosts file. Errors
// are logged but never propagated — DNS will catch up on the next successful
// reload (next Refresh or next reconciler tick).
func (w *Writer) hup(ctx context.Context, logger interface {
	InfoContext(context.Context, string, ...any)
	WarnContext(context.Context, string, ...any)
}) {
	out, err := exec.Command(w.pidofCmd[0], w.pidofCmd[1:]...).Output()
	if err != nil {
		logger.WarnContext(ctx, "pidof dnsmasq failed; skipping reload", "error", err)
		return
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		logger.WarnContext(ctx, "pidof dnsmasq returned no output; skipping reload")
		return
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		logger.WarnContext(ctx, "pidof dnsmasq returned non-numeric pid", "raw", fields[0])
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		logger.WarnContext(ctx, "find dnsmasq process failed", "pid", pid, "error", err)
		return
	}
	if err := proc.Signal(unix.SIGHUP); err != nil {
		logger.WarnContext(ctx, "SIGHUP dnsmasq failed", "pid", pid, "error", err)
		return
	}
	logger.InfoContext(ctx, "dnsmasq reloaded vm-hosts", "pid", pid)
}

func hashContent(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

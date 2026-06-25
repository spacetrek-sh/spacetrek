package hostswriter

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// stubReader is a minimal VMReader for testing.
type stubReader struct {
	vms []*vmdomain.VM
	err error
}

func (s *stubReader) List(_ context.Context) ([]*vmdomain.VM, error) {
	return s.vms, s.err
}

func strPtr(s string) *string { return &s }

func vmWith(name, ip string, status vmdomain.Status) *vmdomain.VM {
	return &vmdomain.VM{
		ID:        name + "-id",
		Name:      name,
		IPAddress: strPtr(ip),
		Status:    status,
	}
}

func TestRender_EmptyAndEligibleFiltering(t *testing.T) {
	cases := []struct {
		name string
		in   []*vmdomain.VM
		want string
	}{
		{
			name: "empty",
			in:   nil,
			want: "# managed by spacetrk orchestrator — do not edit\n",
		},
		{
			name: "skips terminated",
			in:   []*vmdomain.VM{vmWith("dead", "10.200.0.2", vmdomain.StatusTerminated)},
			want: "# managed by spacetrk orchestrator — do not edit\n",
		},
		{
			name: "skips no-ip",
			in:   []*vmdomain.VM{vmWith("noip", "", vmdomain.StatusReady)},
			want: "# managed by spacetrk orchestrator — do not edit\n",
		},
		{
			name: "skips no-name",
			in: []*vmdomain.VM{{
				ID:        "x",
				Name:      "",
				IPAddress: strPtr("10.200.0.2"),
				Status:    vmdomain.StatusReady,
			}},
			want: "# managed by spacetrk orchestrator — do not edit\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := render(c.in)
			if string(got) != c.want {
				t.Errorf("render mismatch.\n got: %q\nwant: %q", got, c.want)
			}
		})
	}
}

func TestRender_SortsByNameAndTwoLabelsPerLine(t *testing.T) {
	vms := []*vmdomain.VM{
		vmWith("zealous-einstein", "10.200.0.4", vmdomain.StatusRunning),
		vmWith("admiring-turing", "10.200.0.2", vmdomain.StatusReady),
		vmWith("nervous-lovelace", "10.200.0.3", vmdomain.StatusIdle),
	}
	got := string(render(vms))

	want := `# managed by spacetrk orchestrator — do not edit
10.200.0.2   admiring-turing.vm.internal	admiring-turing
10.200.0.3   nervous-lovelace.vm.internal	nervous-lovelace
10.200.0.4   zealous-einstein.vm.internal	zealous-einstein
`
	if got != want {
		t.Errorf("render mismatch.\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRefresh_GoldenFile(t *testing.T) {
	dir := t.TempDir()
	hostsPath := filepath.Join(dir, "vm-hosts")

	w := New(&stubReader{vms: []*vmdomain.VM{
		vmWith("nervous-einstein", "10.200.0.2", vmdomain.StatusRunning),
		vmWith("quirky-liskov", "10.200.0.3", vmdomain.StatusReady),
	}}, hostsPath)
	// Replace pidof with a stub that returns no output so HUP is a no-op.
	w.pidofCmd = []string{"true"}

	if err := w.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	got, err := os.ReadFile(hostsPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	want := `# managed by spacetrk orchestrator — do not edit
10.200.0.2   nervous-einstein.vm.internal	nervous-einstein
10.200.0.3   quirky-liskov.vm.internal	quirky-liskov
`
	if !bytes.Equal(got, []byte(want)) {
		t.Errorf("hosts file mismatch.\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRefresh_Idempotent(t *testing.T) {
	dir := t.TempDir()
	hostsPath := filepath.Join(dir, "vm-hosts")

	reader := &stubReader{vms: []*vmdomain.VM{
		vmWith("nervous-einstein", "10.200.0.2", vmdomain.StatusRunning),
	}}

	// pidof stub: fail every time → hup never reaches the success branch,
	// but Refresh should still complete without error. The point is to
	// confirm that the *second* Refresh is a no-op for file writes.
	hupCalls := 0
	w := New(reader, hostsPath)
	w.pidofCmd = []string{"false"} // pidof returns nonzero

	// Track mtime so we can prove the file wasn't rewritten on the second call.
	if err := w.Refresh(context.Background()); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	info1, _ := os.Stat(hostsPath)

	// Sleep a bit so a rewrite would observably change mtime.
	time.Sleep(20 * time.Millisecond)

	if err := w.Refresh(context.Background()); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	info2, _ := os.Stat(hostsPath)

	if info1.ModTime() != info2.ModTime() {
		t.Errorf("idempotency broken: file was rewritten on second Refresh (mtime %s → %s)",
			info1.ModTime(), info2.ModTime())
	}

	_ = hupCalls // not used; pidof=false means HUP never fires
}

func TestRefresh_PicksUpChanges(t *testing.T) {
	dir := t.TempDir()
	hostsPath := filepath.Join(dir, "vm-hosts")

	reader := &stubReader{vms: []*vmdomain.VM{
		vmWith("nervous-einstein", "10.200.0.2", vmdomain.StatusRunning),
	}}
	w := New(reader, hostsPath)
	w.pidofCmd = []string{"false"}

	_ = w.Refresh(context.Background())

	// Add a VM, expect the file to change on next Refresh.
	reader.vms = append(reader.vms,
		vmWith("admiring-turing", "10.200.0.3", vmdomain.StatusReady))
	_ = w.Refresh(context.Background())

	got, _ := os.ReadFile(hostsPath)
	if !strings.Contains(string(got), "admiring-turing") {
		t.Errorf("change not picked up. file:\n%s", got)
	}
	if !strings.Contains(string(got), "nervous-einstein") {
		t.Errorf("existing entry lost. file:\n%s", got)
	}
}

func TestRefresh_DropsTerminated(t *testing.T) {
	dir := t.TempDir()
	hostsPath := filepath.Join(dir, "vm-hosts")

	reader := &stubReader{vms: []*vmdomain.VM{
		vmWith("nervous-einstein", "10.200.0.2", vmdomain.StatusRunning),
	}}
	w := New(reader, hostsPath)
	w.pidofCmd = []string{"false"}

	_ = w.Refresh(context.Background())

	// Terminate the VM, expect the next Refresh to drop it.
	reader.vms[0].Status = vmdomain.StatusTerminated
	_ = w.Refresh(context.Background())

	got, _ := os.ReadFile(hostsPath)
	if strings.Contains(string(got), "nervous-einstein") {
		t.Errorf("terminated VM still in hosts file:\n%s", got)
	}
	if !strings.HasPrefix(string(got), "# managed") {
		t.Errorf("header lost:\n%s", got)
	}
}

func TestRefresh_ReaderErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	w := New(&stubReader{err: errors.New("db down")}, filepath.Join(dir, "vm-hosts"))
	err := w.Refresh(context.Background())
	if err == nil {
		t.Fatal("expected error from reader failure")
	}
}

// quietLogger discards everything; Refresh uses pkglog.FromContext which
// returns a real slog handler — for this test we override via context.
func TestRefresh_QuietWithSlog(t *testing.T) {
	dir := t.TempDir()
	w := New(&stubReader{vms: nil}, filepath.Join(dir, "vm-hosts"))
	w.pidofCmd = []string{"true"}
	ctx := context.Background()
	if err := w.Refresh(ctx); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	_ = slog.Default()
}

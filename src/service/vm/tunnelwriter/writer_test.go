package tunnelwriter

import (
	"bytes"
	"context"
	"errors"
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

func intPtr(i int) *int { return &i }

func vmWith(name, ip string, status vmdomain.Status, port int) *vmdomain.VM {
	return &vmdomain.VM{
		ID:          name + "-id",
		Name:        name,
		IPAddress:   strPtr(ip),
		Status:      status,
		ServicePort: port,
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
			want: "# managed by spacetrk orchestrator — do not edit\ningress:\n  - service: http_status:404\n",
		},
		{
			name: "skips terminated",
			in:   []*vmdomain.VM{vmWith("dead", "10.200.0.2", vmdomain.StatusTerminated, 80)},
			want: "# managed by spacetrk orchestrator — do not edit\ningress:\n  - service: http_status:404\n",
		},
		{
			name: "skips no-ip",
			in:   []*vmdomain.VM{vmWith("noip", "", vmdomain.StatusReady, 80)},
			want: "# managed by spacetrk orchestrator — do not edit\ningress:\n  - service: http_status:404\n",
		},
		{
			name: "skips no-name",
			in: []*vmdomain.VM{{
				ID:          "x",
				Name:        "",
				IPAddress:   strPtr("10.200.0.2"),
				Status:      vmdomain.StatusReady,
				ServicePort: 80,
			}},
			want: "# managed by spacetrk orchestrator — do not edit\ningress:\n  - service: http_status:404\n",
		},
		{
			name: "skips zero-port",
			in: []*vmdomain.VM{{
				ID:          "x",
				Name:        "noport",
				IPAddress:   strPtr("10.200.0.2"),
				Status:      vmdomain.StatusReady,
				ServicePort: 0,
			}},
			want: "# managed by spacetrk orchestrator — do not edit\ningress:\n  - service: http_status:404\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := render(c.in, ".spacetrek.xyz", "", "")
			if string(got) != c.want {
				t.Errorf("render mismatch.\n got: %q\nwant: %q", got, c.want)
			}
		})
	}
}

func TestRender_SortsByNameAndIncludesPort(t *testing.T) {
	vms := []*vmdomain.VM{
		vmWith("zealous-einstein", "10.200.0.4", vmdomain.StatusRunning, 3000),
		vmWith("admiring-turing", "10.200.0.2", vmdomain.StatusReady, 80),
		vmWith("nervous-lovelace", "10.200.0.3", vmdomain.StatusIdle, 8080),
	}
	got := string(render(vms, ".spacetrek.xyz", "", ""))

	want := `# managed by spacetrk orchestrator — do not edit
ingress:
  - hostname: admiring-turing.spacetrek.xyz
    service: http://10.200.0.2:80
  - hostname: nervous-lovelace.spacetrek.xyz
    service: http://10.200.0.3:8080
  - hostname: zealous-einstein.spacetrek.xyz
    service: http://10.200.0.4:3000
  - service: http_status:404
`
	if got != want {
		t.Errorf("render mismatch.\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRender_CustomDomainSuffix(t *testing.T) {
	got := string(render(
		[]*vmdomain.VM{vmWith("nervous-einstein", "10.200.0.2", vmdomain.StatusRunning, 80)},
		".example.com",
		"",
		"",
	))
	if !strings.Contains(got, "hostname: nervous-einstein.example.com") {
		t.Errorf("custom suffix not applied:\n%s", got)
	}
}

func TestRender_IncludesStaticHeader(t *testing.T) {
	header := "tunnel: abc-123\ncredentials-file: /etc/cloudflared/abc-123.json\n"
	got := string(render(
		[]*vmdomain.VM{vmWith("nervous-einstein", "10.200.0.2", vmdomain.StatusRunning, 80)},
		".spacetrek.xyz",
		header,
		"",
	))
	if !strings.Contains(got, "tunnel: abc-123") {
		t.Errorf("header tunnel directive missing:\n%s", got)
	}
	if !strings.Contains(got, "credentials-file: /etc/cloudflared/abc-123.json") {
		t.Errorf("header credentials-file directive missing:\n%s", got)
	}
	// Header must appear before the ingress block.
	headerIdx := strings.Index(got, "tunnel:")
	ingressIdx := strings.Index(got, "ingress:")
	if headerIdx < 0 || ingressIdx < 0 || headerIdx > ingressIdx {
		t.Errorf("header must precede ingress. headerIdx=%d ingressIdx=%d\n%s", headerIdx, ingressIdx, got)
	}
}

func TestRefresh_GoldenFile(t *testing.T) {
	dir := t.TempDir()
	ingressPath := filepath.Join(dir, "cloudflared-ingress.yml")

	w := New(&stubReader{vms: []*vmdomain.VM{
		vmWith("nervous-einstein", "10.200.0.2", vmdomain.StatusRunning, 80),
		vmWith("quirky-liskov", "10.200.0.3", vmdomain.StatusReady, 8080),
	}}, ingressPath, ".spacetrek.xyz", "", "")

	if err := w.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	got, err := os.ReadFile(ingressPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	want := `# managed by spacetrk orchestrator — do not edit
ingress:
  - hostname: nervous-einstein.spacetrek.xyz
    service: http://10.200.0.2:80
  - hostname: quirky-liskov.spacetrek.xyz
    service: http://10.200.0.3:8080
  - service: http_status:404
`
	if !bytes.Equal(got, []byte(want)) {
		t.Errorf("ingress file mismatch.\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRefresh_Idempotent(t *testing.T) {
	dir := t.TempDir()
	ingressPath := filepath.Join(dir, "cloudflared-ingress.yml")

	reader := &stubReader{vms: []*vmdomain.VM{
		vmWith("nervous-einstein", "10.200.0.2", vmdomain.StatusRunning, 80),
	}}
	w := New(reader, ingressPath, ".spacetrek.xyz", "", "")

	if err := w.Refresh(context.Background()); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	info1, _ := os.Stat(ingressPath)

	time.Sleep(20 * time.Millisecond)

	if err := w.Refresh(context.Background()); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	info2, _ := os.Stat(ingressPath)

	if info1.ModTime() != info2.ModTime() {
		t.Errorf("idempotency broken: file was rewritten on second Refresh (mtime %s → %s)",
			info1.ModTime(), info2.ModTime())
	}
}

func TestRefresh_PicksUpChanges(t *testing.T) {
	dir := t.TempDir()
	ingressPath := filepath.Join(dir, "cloudflared-ingress.yml")

	reader := &stubReader{vms: []*vmdomain.VM{
		vmWith("nervous-einstein", "10.200.0.2", vmdomain.StatusRunning, 80),
	}}
	w := New(reader, ingressPath, ".spacetrek.xyz", "", "")

	_ = w.Refresh(context.Background())

	reader.vms = append(reader.vms,
		vmWith("admiring-turing", "10.200.0.3", vmdomain.StatusReady, 80))
	_ = w.Refresh(context.Background())

	got, _ := os.ReadFile(ingressPath)
	if !strings.Contains(string(got), "admiring-turing") {
		t.Errorf("change not picked up. file:\n%s", got)
	}
	if !strings.Contains(string(got), "nervous-einstein") {
		t.Errorf("existing entry lost. file:\n%s", got)
	}
}

func TestRefresh_DropsTerminated(t *testing.T) {
	dir := t.TempDir()
	ingressPath := filepath.Join(dir, "cloudflared-ingress.yml")

	reader := &stubReader{vms: []*vmdomain.VM{
		vmWith("nervous-einstein", "10.200.0.2", vmdomain.StatusRunning, 80),
	}}
	w := New(reader, ingressPath, ".spacetrek.xyz", "", "")

	_ = w.Refresh(context.Background())

	reader.vms[0].Status = vmdomain.StatusTerminated
	_ = w.Refresh(context.Background())

	got, _ := os.ReadFile(ingressPath)
	if strings.Contains(string(got), "nervous-einstein") {
		t.Errorf("terminated VM still in ingress file:\n%s", got)
	}
	if !strings.HasPrefix(string(got), "# managed") {
		t.Errorf("header lost:\n%s", got)
	}
	if !strings.Contains(string(got), "http_status:404") {
		t.Errorf("catch-all rule lost:\n%s", got)
	}
}

func TestRefresh_ReaderErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	w := New(&stubReader{err: errors.New("db down")}, filepath.Join(dir, "cloudflared-ingress.yml"), ".spacetrek.xyz", "", "")
	if err := w.Refresh(context.Background()); err == nil {
		t.Fatal("expected error from reader failure")
	}
}

func TestRender_WithStaticRules(t *testing.T) {
	static := "  - hostname: api.spacetrek.xyz\n    service: http://localhost:8080\n"
	got := string(render(
		[]*vmdomain.VM{vmWith("nervous-einstein", "10.200.0.2", vmdomain.StatusRunning, 80)},
		".box.spacetrek.xyz",
		"",
		static,
	))

	// Static rules must appear after `ingress:` and before the VM entry.
	ingressIdx := strings.Index(got, "ingress:\n")
	staticIdx := strings.Index(got, "hostname: api.spacetrek.xyz")
	vmIdx := strings.Index(got, "hostname: nervous-einstein")
	catchAllIdx := strings.Index(got, "http_status:404")
	if ingressIdx < 0 || staticIdx < 0 || vmIdx < 0 || catchAllIdx < 0 {
		t.Fatalf("missing expected sections:\n%s", got)
	}
	if !(ingressIdx < staticIdx && staticIdx < vmIdx && vmIdx < catchAllIdx) {
		t.Errorf("ordering wrong. ingress=%d static=%d vm=%d catchall=%d\n%s",
			ingressIdx, staticIdx, vmIdx, catchAllIdx, got)
	}
}

func TestRefresh_StaticRulesFileMissing(t *testing.T) {
	dir := t.TempDir()
	ingressPath := filepath.Join(dir, "cloudflared-ingress.yml")
	missingStatic := filepath.Join(dir, "does-not-exist.yml")

	w := New(&stubReader{vms: nil}, ingressPath, ".box.spacetrek.xyz", "", missingStatic)
	if err := w.Refresh(context.Background()); err != nil {
		t.Fatalf("missing static file should not error: %v", err)
	}
	got, _ := os.ReadFile(ingressPath)
	if strings.Contains(string(got), "localhost") {
		t.Errorf("missing static file should produce no static rules:\n%s", got)
	}
}

func TestRefresh_StaticRulesFilePresent(t *testing.T) {
	dir := t.TempDir()
	ingressPath := filepath.Join(dir, "cloudflared-ingress.yml")
	staticPath := filepath.Join(dir, "static.yml")

	static := "  - hostname: api.spacetrek.xyz\n    service: http://localhost:8080\n"
	if err := os.WriteFile(staticPath, []byte(static), 0o600); err != nil {
		t.Fatalf("write static file: %v", err)
	}

	w := New(&stubReader{vms: nil}, ingressPath, ".box.spacetrek.xyz", "", staticPath)
	if err := w.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got, _ := os.ReadFile(ingressPath)
	if !strings.Contains(string(got), "hostname: api.spacetrek.xyz") {
		t.Errorf("static rule missing from rendered file:\n%s", got)
	}
	if !strings.Contains(string(got), "http://localhost:8080") {
		t.Errorf("static service line missing:\n%s", got)
	}
}

func TestRefresh_StaticRulesChangeTriggersRewrite(t *testing.T) {
	dir := t.TempDir()
	ingressPath := filepath.Join(dir, "cloudflared-ingress.yml")
	staticPath := filepath.Join(dir, "static.yml")

	static1 := "  - hostname: api.spacetrek.xyz\n    service: http://localhost:8080\n"
	if err := os.WriteFile(staticPath, []byte(static1), 0o600); err != nil {
		t.Fatalf("write static file: %v", err)
	}

	w := New(&stubReader{vms: nil}, ingressPath, ".box.spacetrek.xyz", "", staticPath)
	_ = w.Refresh(context.Background())
	info1, _ := os.Stat(ingressPath)

	time.Sleep(20 * time.Millisecond)

	// Edit the static file. sha256 of rendered output changes → file rewritten.
	static2 := "  - hostname: www.spacetrek.xyz\n    service: http://localhost:5173\n"
	if err := os.WriteFile(staticPath, []byte(static2), 0o600); err != nil {
		t.Fatalf("rewrite static file: %v", err)
	}
	_ = w.Refresh(context.Background())

	got, _ := os.ReadFile(ingressPath)
	if strings.Contains(string(got), "api.spacetrek.xyz") {
		t.Errorf("stale static rule still present after edit:\n%s", got)
	}
	if !strings.Contains(string(got), "www.spacetrek.xyz") {
		t.Errorf("new static rule not picked up:\n%s", got)
	}
	info2, _ := os.Stat(ingressPath)
	if info1.ModTime() == info2.ModTime() {
		t.Errorf("static edit did not trigger rewrite (mtime unchanged)")
	}
}

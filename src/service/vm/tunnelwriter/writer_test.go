package tunnelwriter

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRender_WildcardRule(t *testing.T) {
	got := string(render(".box.spacetrek.xyz", "172.19.0.4", "", ""))
	want := `# managed by spacetrk orchestrator — do not edit
ingress:
  - hostname: "*.box.spacetrek.xyz"
    service: http://172.19.0.4:8090
  - service: http_status:404
`
	if got != want {
		t.Errorf("render mismatch.\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRender_CustomDomainSuffix(t *testing.T) {
	got := string(render(".example.com", "10.0.0.5", "", ""))
	if !strings.Contains(got, `hostname: "*.example.com"`) {
		t.Errorf("custom suffix not applied:\n%s", got)
	}
}

func TestRender_IncludesStaticHeader(t *testing.T) {
	header := "tunnel: abc-123\ncredentials-file: /etc/cloudflared/abc-123.json\n"
	got := string(render(".box.spacetrek.xyz", "172.19.0.4", header, ""))
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

	w := New(ingressPath, ".box.spacetrek.xyz", "172.19.0.4", "", "")

	if err := w.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh failed: %v", err)
	}

	got, err := os.ReadFile(ingressPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	want := `# managed by spacetrk orchestrator — do not edit
ingress:
  - hostname: "*.box.spacetrek.xyz"
    service: http://172.19.0.4:8090
  - service: http_status:404
`
	if !bytes.Equal(got, []byte(want)) {
		t.Errorf("ingress file mismatch.\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRefresh_Idempotent(t *testing.T) {
	dir := t.TempDir()
	ingressPath := filepath.Join(dir, "cloudflared-ingress.yml")

	w := New(ingressPath, ".box.spacetrek.xyz", "172.19.0.4", "", "")

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

func TestRender_WithStaticRules(t *testing.T) {
	static := "  - hostname: api.spacetrek.xyz\n    service: http://localhost:8080\n"
	got := string(render(".box.spacetrek.xyz", "172.19.0.4", "", static))

	// Static rules must appear after `ingress:` and before the wildcard entry.
	ingressIdx := strings.Index(got, "ingress:\n")
	staticIdx := strings.Index(got, "hostname: api.spacetrek.xyz")
	wildcardIdx := strings.Index(got, `hostname: "*.box.spacetrek.xyz"`)
	catchAllIdx := strings.Index(got, "http_status:404")
	if ingressIdx < 0 || staticIdx < 0 || wildcardIdx < 0 || catchAllIdx < 0 {
		t.Fatalf("missing expected sections:\n%s", got)
	}
	if !(ingressIdx < staticIdx && staticIdx < wildcardIdx && wildcardIdx < catchAllIdx) {
		t.Errorf("ordering wrong. ingress=%d static=%d wildcard=%d catchall=%d\n%s",
			ingressIdx, staticIdx, wildcardIdx, catchAllIdx, got)
	}
}

func TestRefresh_StaticRulesFileMissing(t *testing.T) {
	dir := t.TempDir()
	ingressPath := filepath.Join(dir, "cloudflared-ingress.yml")
	missingStatic := filepath.Join(dir, "does-not-exist.yml")

	w := New(ingressPath, ".box.spacetrek.xyz", "172.19.0.4", "", missingStatic)
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

	w := New(ingressPath, ".box.spacetrek.xyz", "172.19.0.4", "", staticPath)
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

	w := New(ingressPath, ".box.spacetrek.xyz", "172.19.0.4", "", staticPath)
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

package activator

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveVMName(t *testing.T) {
	s := &Server{domainSuffix: ".box.spacetrek.xyz"}

	cases := []struct {
		host     string
		wantName string
		wantOK   bool
	}{
		{"admiring-turing.box.spacetrek.xyz", "admiring-turing", true},
		{"admiring-turing.box.spacetrek.xyz:443", "admiring-turing", true},
		{"a.b.box.spacetrek.xyz", "a.b", true}, // multi-label prefix is fine
		{"admiring-turing.example.com", "", false},
		{"box.spacetrek.xyz", "", false}, // empty name part
		{"", "", false},
	}

	for _, c := range cases {
		t.Run(c.host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Host = c.host
			gotName, gotOK := s.resolveVMName(req)
			if gotName != c.wantName || gotOK != c.wantOK {
				t.Errorf("resolveVMName(host=%q) = (%q,%v), want (%q,%v)",
					c.host, gotName, gotOK, c.wantName, c.wantOK)
			}
		})
	}
}

func TestHealthz(t *testing.T) {
	s := NewServer(Config{
		DomainSuffix: ".box.spacetrek.xyz",
		Logger:       testLogger(),
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("healthz: status = %d, want 200. body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "ok" {
		t.Errorf("healthz: body = %q, want %q", rec.Body.String(), "ok")
	}
}

func TestServeHTTP_HostMismatch_404(t *testing.T) {
	s := NewServer(Config{
		DomainSuffix: ".box.spacetrek.xyz",
		Logger:       testLogger(),
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "something.example.com"
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for non-matching host, got %d", rec.Code)
	}
}

// TestResolveVMName_MeshProxy covers ModeMeshProxy: the HTTP client emits
// absolute-form request lines (GET http://host/path HTTP/1.1), so the
// target host lives in r.URL.Host — r.Host is empty/irrelevant. Also
// confirms the proxy refuses non-mesh destinations so it can't be used
// as an open proxy by a misconfigured NO_PROXY.
func TestResolveVMName_MeshProxy(t *testing.T) {
	s := &Server{mode: ModeMeshProxy, domainSuffix: ".box.spacetrek.xyz"}

	cases := []struct {
		name     string
		url      string
		wantName string
		wantOK   bool
	}{
		{"absolute url with port", "http://admiring-turing.box.spacetrek.xyz:8080/api/users", "admiring-turing", true},
		{"absolute url without port", "http://admiring-turing.box.spacetrek.xyz/api/users", "admiring-turing", true},
		{"multi-label prefix", "http://a.b.box.spacetrek.xyz/", "a.b", true},
		{"non-mesh destination refused", "http://google.com/", "", false},
		{"empty url host refused", "/", "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.url, nil)
			gotName, gotOK := s.resolveVMName(req)
			if gotName != c.wantName || gotOK != c.wantOK {
				t.Errorf("resolveVMName(url=%q) = (%q,%v), want (%q,%v)",
					c.url, gotName, gotOK, c.wantName, c.wantOK)
			}
		})
	}
}

// TestNewServer_DefaultMode confirms that omitting Mode defaults to
// ModeCloudflared — existing deployments don't set ACTIVATOR_MODE.
func TestNewServer_DefaultMode(t *testing.T) {
	s := NewServer(Config{
		DomainSuffix: ".box.spacetrek.xyz",
		Logger:       testLogger(),
	})
	if s.mode != ModeCloudflared {
		t.Errorf("default mode = %q, want %q", s.mode, ModeCloudflared)
	}
}

// Package activator implements the spacetrek-activator: a reverse proxy
// that sits between cloudflared and Firecracker VMs, transparently
// waking idle VMs on incoming requests before forwarding.
//
// The activator runs in its own container with network_mode:
// "service:spacetrek-api", so it shares the orchestrator's network
// namespace. It dials VM IPs directly on 10.200.0.0/16 and calls the
// orchestrator's internal API on localhost:8081.
package activator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OrchestratorClient is the thin HTTP client the activator uses to talk
// to the orchestrator's localhost-bound internal API.
type OrchestratorClient struct {
	baseURL string
	http    *http.Client
}

// NewOrchestratorClient returns a client targeting baseURL (typically
// "http://localhost:8081").
func NewOrchestratorClient(baseURL string) *OrchestratorClient {
	return &OrchestratorClient{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 120 * time.Second, // covers the worst-case cold-start
		},
	}
}

// VMInfo is the subset of VM state the activator needs to route and activate.
type VMInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	IPAddress   string `json:"ip_address"`
	ServicePort int    `json:"service_port"`
	HasSnapshot bool   `json:"has_snapshot"`
}

// LookupVM fetches VM state by friendly name. Returns (nil, true, nil)
// when the VM is unknown or not publicly exposed (service_port == 0).
func (c *OrchestratorClient) LookupVM(ctx context.Context, name string) (*VMInfo, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/internal/v1/vm/by-name/"+name, nil)
	if err != nil {
		return nil, false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("lookup request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, true, fmt.Errorf("lookup %s: status %d: %s", name, resp.StatusCode, body)
	}

	var envelope struct {
		Data VMInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, true, fmt.Errorf("decode lookup response: %w", err)
	}
	return &envelope.Data, true, nil
}

// ResumeVM triggers a blocking ResumeVM in the orchestrator. Returns when
// the VM is running (or fails).
func (c *OrchestratorClient) ResumeVM(ctx context.Context, vmID string) (*VMInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/internal/v1/vm/"+vmID+"/resume", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("resume request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("resume %s: status %d: %s", vmID, resp.StatusCode, body)
	}

	var envelope struct {
		Data VMInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode resume response: %w", err)
	}
	return &envelope.Data, nil
}

// Touch fires-and-forgets a MarkActive call. Errors are swallowed — the
// activator's routing is not affected by whether the touch succeeded.
func (c *OrchestratorClient) Touch(ctx context.Context, vmID string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/internal/v1/vm/"+vmID+"/touch", nil)
	if err != nil {
		return
	}
	// Short timeout — touch should never block the caller.
	touchCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req = req.WithContext(touchCtx)

	resp, err := c.http.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

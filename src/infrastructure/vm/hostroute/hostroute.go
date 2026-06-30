// Package hostroute installs a host-side route into the VM subnet so that
// processes running in the host netns (notably cloudflared on systemd) can
// dial VM IPs directly. The orchestrator container shares the host PID
// namespace (pid: host in docker-compose.yml); this lets it nsenter into
// PID 1's netns and run `ip route replace ...` there.
package hostroute

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
)

// EnsureRoute installs `subnet via <own-eth0-ip>` on the host via nsenter.
// Idempotent — `ip route replace` overwrites any existing route for the
// subnet. Safe to call on every startup.
//
// Requires:
//   - The orchestrator container runs with pid: host.
//   - nsenter is available in the container PATH (debian:12-slim ships it
//     via util-linux; the project's Dockerfile installs iproute2 which
//     pulls it in transitively).
//
// Errors are returned but should not abort startup — VMs work fine without
// host reachability; only cloudflared-on-host depends on the route.
func EnsureRoute(ctx context.Context, subnet string) error {
	logger := pkglog.FromContext(ctx)

	ownIP, err := OwnContainerIP()
	if err != nil {
		return fmt.Errorf("detect own eth0 IP: %w", err)
	}

	// nsenter -t 1 -n   → enter PID 1's network namespace (the host's).
	// ip route replace  → upsert; no error if a route already exists.
	cmd := exec.CommandContext(ctx, "nsenter", "-t", "1", "-n",
		"ip", "route", "replace", subnet, "via", ownIP)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nsenter ip route replace %s via %s: %w: %s",
			subnet, ownIP, err, strings.TrimSpace(string(out)))
	}

	logger.InfoContext(ctx, "host route installed for VM subnet",
		"subnet", subnet, "via", ownIP)
	return nil
}

// OwnContainerIP returns the first non-loopback IPv4 address assigned to
// any interface in the orchestrator's netns. This is the IP the host will
// route VM-subnet traffic to.
func OwnContainerIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", fmt.Errorf("enumerate interface addrs: %w", err)
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		ip := ipNet.IP.To4()
		if ip == nil {
			continue
		}
		return ip.String(), nil
	}
	return "", fmt.Errorf("no non-loopback IPv4 address found")
}

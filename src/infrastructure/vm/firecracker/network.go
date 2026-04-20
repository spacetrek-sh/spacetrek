package firecracker

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// NetworkConfig defines the VM network topology (TAP, NAT).
type NetworkConfig struct {
	BridgeName string // unused, kept for config compatibility
	Subnet     string // CIDR, e.g. 10.200.0.0/16
	GatewayIP  string // IP assigned to each TAP as point-to-point gateway
	DNSIP      string // DNS resolver IP
	IPStart    string // First allocatable IP
	IPEnd      string // Last allocatable IP
	EnableNAT  bool
}

// NetworkManager manages host-side Linux networking for VMs (TAP, NAT).
// Uses point-to-point TAP routing instead of a bridge to avoid br_netfilter
// conflicts in containerized environments (Docker).
type NetworkManager struct {
	cfg      NetworkConfig
	outbound string // auto-detected from /proc/net/route
	mask     string // netmask derived from subnet, e.g. 255.255.0.0
}

// NewNetworkManager creates a NetworkManager and auto-detects the outbound interface.
func NewNetworkManager(cfg NetworkConfig) (*NetworkManager, error) {
	outbound, err := outboundIface()
	if err != nil {
		return nil, fmt.Errorf("detect outbound interface: %w", err)
	}

	_, cidr, err := net.ParseCIDR(cfg.Subnet)
	if err != nil {
		return nil, fmt.Errorf("parse subnet %q: %w", cfg.Subnet, err)
	}
	mask := fmt.Sprintf("%d.%d.%d.%d", cidr.Mask[0], cidr.Mask[1], cidr.Mask[2], cidr.Mask[3])

	return &NetworkManager{cfg: cfg, outbound: outbound, mask: mask}, nil
}

// EnsureBridge is a no-op. Kept for interface compatibility; bridge is not used.
func (m *NetworkManager) EnsureBridge() error {
	return nil
}

// EnsureNAT sets up nft MASQUERADE and forwarding rules (idempotent).
func (m *NetworkManager) EnsureNAT() error {
	if !m.cfg.EnableNAT {
		return nil
	}

	// Enable IP forwarding (skip if read-only, e.g. container with sysctls).
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644); err != nil {
		_ = exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1").Run()
	}

	// Create nft table for VM NAT and forwarding.
	// Matches traffic from the VM subnet going out the default interface.
	nftRuleset := fmt.Sprintf(`
table ip spacetrk-nat {
	chain forward-vm-out {
		type filter hook forward priority filter; policy accept;
		ip saddr %s oifname "%s" counter accept
	}
	chain forward-vm-in {
		type filter hook forward priority filter + 1; policy accept;
		ip daddr %s iifname "%s" ct state established,related counter accept
	}
	chain postrouting {
		type nat hook postrouting priority srcnat; policy accept;
		ip saddr %s oifname "%s" counter masquerade
	}
}
`, m.cfg.Subnet, m.outbound, m.cfg.Subnet, m.outbound, m.cfg.Subnet, m.outbound)

	_ = exec.Command("nft", "delete", "table", "ip", "spacetrk-nat").Run()

	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = strings.NewReader(nftRuleset)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft nat rules: %w: %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

// EnsureDNSReady verifies that the configured DNS resolver is reachable.
func (m *NetworkManager) EnsureDNSReady(timeout time.Duration) error {
	targetIP := m.cfg.DNSIP
	if targetIP == "" {
		targetIP = "127.0.0.1"
	}

	return ensureDNSReadyAddr(targetIP, timeout)
}

// EnsureLocalDNSReady verifies that a local DNS resolver is running.
func (m *NetworkManager) EnsureLocalDNSReady(timeout time.Duration) error {
	return ensureDNSReadyAddr("127.0.0.1", timeout)
}

func ensureDNSReadyAddr(targetIP string, timeout time.Duration) error {
	addr := net.JoinHostPort(targetIP, "53")
	deadline := time.Now().Add(timeout)
	var lastErr error

	for {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err

		if time.Now().After(deadline) {
			return fmt.Errorf("dns resolver %s not reachable: %w", addr, lastErr)
		}

		time.Sleep(250 * time.Millisecond)
	}
}

// CreateTAP creates a TAP device with point-to-point routing to the VM.
// Instead of attaching to a bridge, the gateway IP is assigned directly to the TAP,
// and a host route directs the VM's IP through the TAP device.
// Proxy ARP is enabled so the VM can discover the gateway MAC.
func (m *NetworkManager) CreateTAP(tapName string) error {
	// Remove stale TAP from crashed/restarted runs. Ignore errors when it does not exist.
	_ = run("ip", "link", "del", tapName)

	if err := run("ip", "tuntap", "add", "dev", tapName, "mode", "tap"); err != nil {
		return fmt.Errorf("create tap %s: %w", tapName, err)
	}
	if err := run("ip", "link", "set", tapName, "up"); err != nil {
		_ = run("ip", "link", "del", tapName)
		return fmt.Errorf("bring tap %s up: %w", tapName, err)
	}
	return nil
}

// ConfigureTAP sets up the point-to-point network for a specific VM's TAP device.
// Must be called after CreateTAP with the VM's allocated IP.
func (m *NetworkManager) ConfigureTAP(tapName, vmIP string) error {
	// Assign gateway IP with /32 (point-to-point) to the TAP device.
	if err := run("ip", "addr", "add", m.cfg.GatewayIP+"/32", "dev", tapName); err != nil {
		if !existsAlready(err) {
			return fmt.Errorf("assign gateway to tap %s: %w", tapName, err)
		}
	}

	// Route the VM's IP through this TAP device.
	// Use replace to recover cleanly when stale routes remain after container/process restarts.
	if err := run("ip", "route", "replace", vmIP+"/32", "dev", tapName); err != nil {
		return fmt.Errorf("route %s via tap %s: %w", vmIP, tapName, err)
	}

	// Enable proxy ARP so the VM can resolve the gateway MAC.
	_ = os.WriteFile("/proc/sys/net/ipv4/conf/"+tapName+"/proxy_arp", []byte("1"), 0644)

	// Enable forwarding on this interface.
	_ = os.WriteFile("/proc/sys/net/ipv4/conf/"+tapName+"/forwarding", []byte("1"), 0644)

	return nil
}

// DestroyTAP removes a TAP device and cleans up routes.
func (m *NetworkManager) DestroyTAP(tapName string) error {
	return run("ip", "link", "del", tapName)
}

// BuildIPKernelArg returns the kernel ip= boot argument for a guest VM.
// Format: ip=<client-ip>::<gw>:<netmask>::eth0:off
func (m *NetworkManager) BuildIPKernelArg(ip string) string {
	return fmt.Sprintf("ip=%s::%s:%s::eth0:off", ip, m.cfg.GatewayIP, m.mask)
}

// TAPName returns the TAP device name for a VM ID.
// Linux interface names are limited to 15 characters.
func TAPName(vmID string) string {
	if len(vmID) > 8 {
		return "tap-" + vmID[:8]
	}
	return "tap-" + vmID
}

// outboundIface reads /proc/net/route and returns the interface with the default route.
func outboundIface() (string, error) {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return "", fmt.Errorf("open /proc/net/route: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		iface := fields[0]
		dest := fields[1]
		if dest == "00000000" {
			return iface, nil
		}
	}

	return "", fmt.Errorf("no default route found in /proc/net/route")
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func existsAlready(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already exists")
}

func maskBits(subnet string) string {
	_, cidr, err := net.ParseCIDR(subnet)
	if err != nil {
		return "24"
	}
	ones, _ := cidr.Mask.Size()
	return fmt.Sprintf("%d", ones)
}

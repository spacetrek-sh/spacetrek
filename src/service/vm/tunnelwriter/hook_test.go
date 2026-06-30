package tunnelwriter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
	vmsvc "github.com/spacetrek-sh/spacetrek/src/service/vm"
)

// TestHook_OnVMEvent_TriggersRefresh verifies that firing an event through
// the LifecycleHook interface causes the Writer to re-render the ingress
// file. With the wildcard-collapse refactor, the rendered file no longer
// depends on VM state — but the hook must still fire Refresh so that
// config changes (e.g. orchestrator IP detection lagging behind on boot)
// get picked up when lifecycle events arrive.
func TestHook_OnVMEvent_TriggersRefresh(t *testing.T) {
	dir := t.TempDir()
	ingressPath := filepath.Join(dir, "cloudflared-ingress.yml")

	vm := &vmdomain.VM{ID: "vm-1", Name: "admiring-turing", Status: vmdomain.StatusRunning}
	writer := New(ingressPath, ".box.spacetrek.xyz", "172.19.0.4", "", "")
	hook := &Hook{W: writer}

	if _, err := os.Stat(ingressPath); !os.IsNotExist(err) {
		t.Fatalf("ingress file should not exist before any event, got err=%v", err)
	}

	hook.OnVMEvent(context.Background(), vmsvc.NewAssignedEvent(vm, "chat-1"))

	b, err := os.ReadFile(ingressPath)
	if err != nil {
		t.Fatalf("expected ingress file after event: %v", err)
	}
	if !strings.Contains(string(b), `hostname: "*.box.spacetrek.xyz"`) {
		t.Errorf("ingress file missing wildcard rule after AssignedEvent:\n%s", b)
	}
}

// TestHook_SatisfiesLifecycleHookInterface is a compile-time guarantee that
// *Hook implements vm.LifecycleHook.
func TestHook_SatisfiesLifecycleHookInterface(t *testing.T) {
	var _ vmsvc.LifecycleHook = (*Hook)(nil)
}

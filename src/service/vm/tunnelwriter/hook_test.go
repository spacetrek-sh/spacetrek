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

// TestHook_OnVMEvent_TriggersRefresh is the regression test for the
// "stopped VMs stick in cloudflared ingress" bug class. Verifies that
// firing an event through the LifecycleHook interface causes the Writer
// to re-render the ingress file. Before the typed-event refactor the
// Hook only fired from Create/Destroy — Stop or idle-reap left stale
// entries.
func TestHook_OnVMEvent_TriggersRefresh(t *testing.T) {
	dir := t.TempDir()
	ingressPath := filepath.Join(dir, "cloudflared-ingress.yml")

	vm := vmWith("admiring-turing", "10.200.0.2", vmdomain.StatusRunning, 8000)
	writer := New(&stubReader{vms: []*vmdomain.VM{vm}}, ingressPath, ".box.spacetrek.xyz", "", "")
	hook := &Hook{W: writer}

	if _, err := os.Stat(ingressPath); !os.IsNotExist(err) {
		t.Fatalf("ingress file should not exist before any event, got err=%v", err)
	}

	hook.OnVMEvent(context.Background(), vmsvc.NewAssignedEvent(vm, "chat-1"))

	b, err := os.ReadFile(ingressPath)
	if err != nil {
		t.Fatalf("expected ingress file after event: %v", err)
	}
	if !strings.Contains(string(b), "admiring-turing.box.spacetrek.xyz") {
		t.Errorf("ingress file missing VM entry after AssignedEvent:\n%s", b)
	}

	// VM disappears from repo (Destroy) — Hook must Refresh and drop the entry.
	writer.reader = &stubReader{vms: nil}
	hook.OnVMEvent(context.Background(), vmsvc.NewDestroyedEvent(vm))

	b, err = os.ReadFile(ingressPath)
	if err != nil {
		t.Fatalf("expected ingress file after DestroyedEvent: %v", err)
	}
	if strings.Contains(string(b), "admiring-turing") {
		t.Errorf("ingress file still contains destroyed VM:\n%s", b)
	}
}

// TestHook_SatisfiesLifecycleHookInterface is a compile-time guarantee that
// *Hook implements vm.LifecycleHook.
func TestHook_SatisfiesLifecycleHookInterface(t *testing.T) {
	var _ vmsvc.LifecycleHook = (*Hook)(nil)
}

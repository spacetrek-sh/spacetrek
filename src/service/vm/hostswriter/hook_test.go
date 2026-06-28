package hostswriter

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
// "stopped VMs stick in DNS" bug class. It verifies that an event fired
// through the LifecycleHook interface causes the Writer to re-render the
// hosts file. Before the typed-event refactor the Hook only fired from
// Create/Destroy — a Stop or idle-reap left stale entries in the file
// because Refresh was never invoked.
func TestHook_OnVMEvent_TriggersRefresh(t *testing.T) {
	dir := t.TempDir()
	hostsPath := filepath.Join(dir, "vm-hosts")

	vm := vmWith("admiring-turing", "10.200.0.2", vmdomain.StatusRunning)
	writer := New(&stubReader{vms: []*vmdomain.VM{vm}}, hostsPath)
	hook := &Hook{W: writer}

	// Sanity: nothing rendered yet.
	if _, err := os.Stat(hostsPath); !os.IsNotExist(err) {
		t.Fatalf("hosts file should not exist before any event fires, got err=%v", err)
	}

	// Fire an AssignedEvent — Hook must call Refresh, which renders the file.
	hook.OnVMEvent(context.Background(), vmsvc.NewAssignedEvent(vm, "chat-1"))

	b, err := os.ReadFile(hostsPath)
	if err != nil {
		t.Fatalf("expected hosts file after event: %v", err)
	}
	if !strings.Contains(string(b), "admiring-turing.vm.internal") {
		t.Errorf("hosts file missing VM entry after AssignedEvent:\n%s", b)
	}

	// Subsequent events re-render. Simulate the VM disappearing from the
	// repo (e.g. after Destroy) and fire a DestroyedEvent — Hook must
	// Refresh again and the entry must drop.
	writer.reader = &stubReader{vms: nil}
	hook.OnVMEvent(context.Background(), vmsvc.NewDestroyedEvent(vm))

	b, err = os.ReadFile(hostsPath)
	if err != nil {
		t.Fatalf("expected hosts file after DestroyedEvent: %v", err)
	}
	if strings.Contains(string(b), "admiring-turing") {
		t.Errorf("hosts file still contains destroyed VM:\n%s", b)
	}
}

// TestHook_SatisfiesLifecycleHookInterface is a compile-time guarantee that
// *Hook implements vm.LifecycleHook. If the interface drifts, this fails to
// compile and we know to update the subscriber.
func TestHook_SatisfiesLifecycleHookInterface(t *testing.T) {
	var _ vmsvc.LifecycleHook = (*Hook)(nil)
}

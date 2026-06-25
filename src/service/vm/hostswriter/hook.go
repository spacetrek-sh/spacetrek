package hostswriter

import (
	"context"

	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// Hook adapts a *Writer to the vm.Service LifecycleHook interface. It is a
// separate type so the vm package doesn't need to import hostswriter —
// main.go wires it up.
type Hook struct {
	W *Writer
}

// OnVMChanged is called inline by the VM service after lifecycle transitions.
// The VM argument is informational (the writer always rebuilds from the repo
// to avoid missing updates from concurrent events).
func (h *Hook) OnVMChanged(ctx context.Context, _ *vmdomain.VM) {
	_ = h.W.Refresh(ctx)
}

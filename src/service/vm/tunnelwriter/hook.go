package tunnelwriter

import (
	"context"

	vmsvc "github.com/spacetrek-sh/spacetrek/src/service/vm"
)

// Hook adapts a *Writer to the vm.Service LifecycleHook interface. It is a
// separate type so the vm package doesn't need to import tunnelwriter —
// main.go wires it up.
type Hook struct {
	W *Writer
}

// OnVMEvent is called inline by the VM service after lifecycle transitions.
// The event argument is informational (the writer always rebuilds from the
// repo to avoid missing updates from concurrent events).
func (h *Hook) OnVMEvent(ctx context.Context, _ vmsvc.Event) {
	_ = h.W.Refresh(ctx)
}

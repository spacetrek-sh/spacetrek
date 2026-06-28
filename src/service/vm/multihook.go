package vm

import (
	"context"

	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// multiHook fans out OnVMChanged to multiple LifecycleHook implementations.
// Returned by MultiHook; used when more than one subsystem needs to react to
// VM lifecycle transitions (e.g. hostswriter + tunnelwriter).
type multiHook struct {
	hooks []LifecycleHook
}

// MultiHook returns a LifecycleHook that dispatches to every hook passed in.
// Order is preserved. Nil hooks are skipped.
func MultiHook(hooks ...LifecycleHook) LifecycleHook {
	filtered := make([]LifecycleHook, 0, len(hooks))
	for _, h := range hooks {
		if h != nil {
			filtered = append(filtered, h)
		}
	}
	return multiHook{hooks: filtered}
}

func (m multiHook) OnVMChanged(ctx context.Context, vm *vmdomain.VM) {
	for _, h := range m.hooks {
		h.OnVMChanged(ctx, vm)
	}
}

package toolsvc

import (
	"context"

	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	"github.com/kumori-sh/spacetrk/src/core/domain/tool"
	vmdomain "github.com/kumori-sh/spacetrk/src/core/domain/vm"
)

// VMLister is the subset of VM service needed by the list tool.
type VMLister interface {
	ListActiveLeasesByChat(ctx context.Context, chatID string) ([]vmdomain.Lease, error)
	Get(ctx context.Context, id string) (*vmdomain.VM, error)
}

// VMListTool lists all VMs currently assigned to the chat.
type VMListTool struct {
	lister VMLister
}

func NewVMListTool(lister VMLister) *VMListTool {
	return &VMListTool{lister: lister}
}

func (t *VMListTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "vm.list",
		Description: "List all VMs currently assigned to this conversation. Returns VM IDs and statuses so you know which vm_id to use with vm.execute_command.",
		Parameters:  map[string]tool.Parameter{},
	}
}

func (t *VMListTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	logger := pkglog.FromContext(ctx)
	result := tool.Result{ToolCallID: call.ID, ToolName: call.Name}

	chatID, ok := tool.ChatIDFromContext(ctx)
	if !ok {
		result.OK = false
		result.Error = "chat_id not available in context"
		return result, nil
	}

	leases, err := t.lister.ListActiveLeasesByChat(ctx, chatID)
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		logger.ErrorContext(ctx, "vm list tool failed", "chat_id", chatID, "error", err)
		return result, nil
	}

	vms := make([]map[string]any, 0, len(leases))
	for _, lease := range leases {
		vm, err := t.lister.Get(ctx, lease.VMID)
		if err != nil {
			logger.WarnContext(ctx, "vm list tool: failed to get vm", "vm_id", lease.VMID, "error", err)
			continue
		}
		vms = append(vms, map[string]any{
			"vm_id":    vm.ID,
			"status":   string(vm.Status),
			"provider": string(vm.Provider),
		})
	}

	result.OK = true
	result.Payload = map[string]any{"vms": vms}
	logger.DebugContext(ctx, "vm list tool: returned vms", "chat_id", chatID, "count", len(vms))
	return result, nil
}

var _ tool.Tool = (*VMListTool)(nil)

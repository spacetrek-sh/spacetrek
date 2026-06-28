package toolsvc

import (
	"context"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// VMLister is the subset of VM service needed by the list tool.
type VMLister interface {
	ListActiveLeasesByChat(ctx context.Context, chatID string) ([]vmdomain.Lease, error)
	ListPreviousLeasesForChat(ctx context.Context, chatID string) ([]*vmdomain.VM, error)
	Get(ctx context.Context, id string) (*vmdomain.VM, error)
	HasSnapshot(ctx context.Context, vmID string) bool
}

// VMListTool lists all VMs currently assigned to the chat and previously used VMs.
type VMListTool struct {
	lister VMLister
}

func NewVMListTool(lister VMLister) *VMListTool {
	return &VMListTool{lister: lister}
}

func (t *VMListTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "vm.list",
		Description: "List all VMs for this conversation. Returns currently active VMs and previously used VMs that can be restored. Use the vm_id from the results with vm.start to restore a specific VM.",
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

	// Active leases.
	leases, err := t.lister.ListActiveLeasesByChat(ctx, chatID)
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		logger.ErrorContext(ctx, "vm list tool failed", "chat_id", chatID, "error", err)
		return result, nil
	}

	active := make([]map[string]any, 0, len(leases))
	for _, lease := range leases {
		vm, err := t.lister.Get(ctx, lease.VMID)
		if err != nil {
			logger.WarnContext(ctx, "vm list tool: failed to get vm", "vm_id", lease.VMID, "error", err)
			continue
		}
		active = append(active, map[string]any{
			"vm_id":        vm.ID,
			"name":         vm.Name,
			"status":       string(vm.Status),
			"provider":     string(vm.Provider),
			"service_port": vm.ServicePort,
			"public_url":   publicURL(vm),
		})
	}

	// Previous VMs that can be restored.
	previous := make([]map[string]any, 0)
	prevVMs, err := t.lister.ListPreviousLeasesForChat(ctx, chatID)
	if err != nil {
		logger.DebugContext(ctx, "vm list tool: no previous VMs found", "chat_id", chatID, "error", err)
	}
	for _, vm := range prevVMs {
		previous = append(previous, map[string]any{
			"vm_id":        vm.ID,
			"name":         vm.Name,
			"status":       string(vm.Status),
			"provider":     string(vm.Provider),
			"service_port": vm.ServicePort,
			"public_url":   publicURL(vm),
			"has_snapshot": t.lister.HasSnapshot(ctx, vm.ID),
		})
	}

	result.OK = true
	result.Payload = map[string]any{
		"active":   active,
		"previous": previous,
	}
	logger.DebugContext(ctx, "vm list tool: returned vms", "chat_id", chatID, "active", len(active), "previous", len(previous))
	return result, nil
}

var _ tool.Tool = (*VMListTool)(nil)

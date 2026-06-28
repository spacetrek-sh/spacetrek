package toolsvc

import (
	"context"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// VMRestarter is the subset of VM service needed by the start tool.
type VMRestarter interface {
	FindPreviousLeaseForChat(ctx context.Context, chatID string) (*vmdomain.VM, error)
	Get(ctx context.Context, id string) (*vmdomain.VM, error)
	AssignToChat(ctx context.Context, vmID, chatID string) (*vmdomain.VM, error)
	HasSnapshot(ctx context.Context, vmID string) bool
	ResumeVM(ctx context.Context, vmID, chatID string) (*vmdomain.VM, error)
	ResolveEnvironmentHint(ctx context.Context, vmID string) (string, error)
}

// VMStartTool finds a previously used VM for the chat and reassigns it.
// If the VM has a snapshot and is not running, it restores from snapshot.
type VMStartTool struct {
	restarter VMRestarter
}

func NewVMStartTool(restarter VMRestarter) *VMStartTool {
	return &VMStartTool{restarter: restarter}
}

func (t *VMStartTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "vm.start",
		Description: "Resume a VM for this conversation. Without vm_id, restores the most recent VM. With vm_id, restores that specific VM. If the VM has a snapshot and is not running, it will be restored from snapshot with its previous filesystem state. Use vm.list to discover available VMs.",
		Parameters: map[string]tool.Parameter{
			"vm_id": {
				Type:        "string",
				Description: "Optional ID of a specific VM to restore. If omitted, the most recent VM is used.",
				Required:    false,
			},
		},
	}
}

func (t *VMStartTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	logger := pkglog.FromContext(ctx)
	result := tool.Result{ToolCallID: call.ID, ToolName: call.Name}

	chatID, ok := tool.ChatIDFromContext(ctx)
	if !ok {
		result.OK = false
		result.Error = "chat_id not available in context"
		return result, nil
	}

	var vm *vmdomain.VM

	// Check if a specific vm_id was provided.
	if vmID, found := readStringArg(call.Arguments, "vm_id"); found {
		var err error
		vm, err = t.restarter.Get(ctx, vmID)
		if err != nil {
			result.OK = false
			result.Error = "VM not found: " + vmID
			logger.DebugContext(ctx, "vm start tool: specific vm not found", "vm_id", vmID, "error", err)
			return result, nil
		}
	} else {
		// Default: find the most recent VM for this chat.
		var err error
		vm, err = t.restarter.FindPreviousLeaseForChat(ctx, chatID)
		if err != nil {
			result.OK = false
			result.Error = "No previous VM found for this chat. Use vm.create to create a new one."
			logger.DebugContext(ctx, "vm start tool: no previous vm found", "chat_id", chatID, "error", err)
			return result, nil
		}
	}

	// If the VM has a snapshot and is not running, restore from snapshot.
	if t.restarter.HasSnapshot(ctx, vm.ID) && !vm.Status.IsActive() {
		logger.InfoContext(ctx, "vm start tool: restoring from snapshot", "vm_id", vm.ID, "chat_id", chatID)
		assigned, err := t.restarter.ResumeVM(ctx, vm.ID, chatID)
		if err != nil {
			result.OK = false
			result.Error = "Failed to restore VM from snapshot: " + err.Error()
			logger.ErrorContext(ctx, "vm start tool: restore from snapshot failed", "vm_id", vm.ID, "error", err)
			return result, nil
		}

		result.OK = true
		result.Payload = enrichedPayload(assigned, t.restarter, ctx, true)
		logger.InfoContext(ctx, "vm start tool: restored from snapshot", "vm_id", assigned.ID, "chat_id", chatID)
		return result, nil
	}

	assigned, err := t.restarter.AssignToChat(ctx, vm.ID, chatID)
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		logger.ErrorContext(ctx, "vm start tool: assign failed", "vm_id", vm.ID, "error", err)
		return result, nil
	}

	result.OK = true
	result.Payload = enrichedPayload(assigned, t.restarter, ctx, false)
	logger.InfoContext(ctx, "vm start tool: reassigned previous vm", "vm_id", assigned.ID, "chat_id", chatID)
	return result, nil
}

// enrichedPayload builds a tool result payload with the environment type name.
func enrichedPayload(vm *vmdomain.VM, restarter VMRestarter, ctx context.Context, restored bool) map[string]any {
	payload := map[string]any{
		"vm_id":        vm.ID,
		"name":         vm.Name,
		"status":       string(vm.Status),
		"service_port": vm.ServicePort,
		"public_url":   publicURL(vm),
	}
	if restored {
		payload["restored"] = true
	}
	if envType, err := restarter.ResolveEnvironmentHint(ctx, vm.ID); err == nil && envType != "" {
		payload["environment"] = envType
	}
	return payload
}

var _ tool.Tool = (*VMStartTool)(nil)

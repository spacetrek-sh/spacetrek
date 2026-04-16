package toolsvc

import (
	"context"

	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	"github.com/kumori-sh/spacetrk/src/core/domain/tool"
	vmdomain "github.com/kumori-sh/spacetrk/src/core/domain/vm"
)

// VMRestarter is the subset of VM service needed by the start tool.
type VMRestarter interface {
	FindPreviousLeaseForChat(ctx context.Context, chatID string) (*vmdomain.VM, error)
	AssignToChat(ctx context.Context, vmID, chatID string) (*vmdomain.VM, error)
}

// VMStartTool finds a previously used VM for the chat and reassigns it.
type VMStartTool struct {
	restarter VMRestarter
}

func NewVMStartTool(restarter VMRestarter) *VMStartTool {
	return &VMStartTool{restarter: restarter}
}

func (t *VMStartTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "vm.start",
		Description: "Resume a VM previously used in this conversation. Finds the most recent idle or ready VM from this chat's history and reassigns it. Use vm.list first to check if any VMs are already running.",
		Parameters:  map[string]tool.Parameter{},
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

	vm, err := t.restarter.FindPreviousLeaseForChat(ctx, chatID)
	if err != nil {
		result.OK = false
		result.Error = "No previous VM found for this chat. Use vm.create to create a new one."
		logger.DebugContext(ctx, "vm start tool: no previous vm found", "chat_id", chatID, "error", err)
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
	result.Payload = map[string]any{
		"vm_id":    assigned.ID,
		"status":   string(assigned.Status),
		"provider": string(assigned.Provider),
	}
	logger.InfoContext(ctx, "vm start tool: reassigned previous vm", "vm_id", assigned.ID, "chat_id", chatID)
	return result, nil
}

var _ tool.Tool = (*VMStartTool)(nil)

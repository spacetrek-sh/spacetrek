package toolsvc

import (
	"context"

	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	"github.com/kumori-sh/spacetrk/src/core/domain/tool"
	vmdomain "github.com/kumori-sh/spacetrk/src/core/domain/vm"
)

// VMStopper is the subset of VM service needed by the stop tool.
type VMStopper interface {
	Stop(ctx context.Context, id string) (*vmdomain.VM, error)
}

// VMStopTool stops a VM and releases it from the chat.
type VMStopTool struct {
	stopper VMStopper
}

func NewVMStopTool(stopper VMStopper) *VMStopTool {
	return &VMStopTool{stopper: stopper}
}

func (t *VMStopTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "vm.stop",
		Description: "Stop a VM that is currently running in this conversation. The VM will be released from the chat.",
		Parameters: map[string]tool.Parameter{
			"vm_id": {
				Type:        "string",
				Description: "ID of the VM to stop",
				Required:    true,
			},
		},
	}
}

func (t *VMStopTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	logger := pkglog.FromContext(ctx)
	result := tool.Result{ToolCallID: call.ID, ToolName: call.Name}

	vmID, ok := readStringArg(call.Arguments, "vm_id")
	if !ok {
		result.OK = false
		result.Error = "missing required argument vm_id"
		return result, nil
	}

	vm, err := t.stopper.Stop(ctx, vmID)
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		logger.ErrorContext(ctx, "vm stop tool failed", "vm_id", vmID, "error", err)
		return result, nil
	}

	result.OK = true
	result.Payload = map[string]any{
		"vm_id":  vm.ID,
		"status": string(vm.Status),
	}
	logger.InfoContext(ctx, "vm stop tool: stopped", "vm_id", vm.ID)
	return result, nil
}

var _ tool.Tool = (*VMStopTool)(nil)

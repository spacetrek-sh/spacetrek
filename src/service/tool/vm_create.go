package toolsvc

import (
	"context"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// VMCreator is the subset of VM service needed by the create tool.
type VMCreator interface {
	ResolveEnvironment(ctx context.Context, envType string) (string, error)
	Create(ctx context.Context, envID, conversationID string, provider vmdomain.Provider, workspaceSizeGB int, vcpu, memoryMB, diskMB *int) (*vmdomain.VM, error)
	AssignToChat(ctx context.Context, vmID, chatID string) (*vmdomain.VM, error)
}

// VMCreateTool creates a new microVM and assigns it to the current chat.
type VMCreateTool struct {
	creator VMCreator
}

func NewVMCreateTool(creator VMCreator) *VMCreateTool {
	return &VMCreateTool{creator: creator}
}

func (t *VMCreateTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "vm.create",
		Description: "Create a new microVM and assign it to the current conversation. Returns the VM ID for use with vm.execute_command.",
		Parameters: map[string]tool.Parameter{
			"environment": {
				Type:        "string",
				Description: "Environment type for the VM (e.g., 'alpine', 'ubuntu')",
				Required:    true,
			},
		},
	}
}

func (t *VMCreateTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	logger := pkglog.FromContext(ctx)
	result := tool.Result{ToolCallID: call.ID, ToolName: call.Name}

	chatID, ok := tool.ChatIDFromContext(ctx)
	if !ok {
		result.OK = false
		result.Error = "chat_id not available in context"
		return result, nil
	}

	envType, ok := readStringArg(call.Arguments, "environment")
	if !ok {
		result.OK = false
		result.Error = "missing required argument environment"
		return result, nil
	}

	envID, err := t.creator.ResolveEnvironment(ctx, envType)
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		logger.ErrorContext(ctx, "vm create tool: environment resolve failed", "env_type", envType, "error", err)
		return result, nil
	}

	vm, err := t.creator.Create(ctx, envID, chatID, vmdomain.ProviderFirecracker, 2, nil, nil, nil)
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		logger.ErrorContext(ctx, "vm create tool failed", "error", err)
		return result, nil
	}

	assigned, err := t.creator.AssignToChat(ctx, vm.ID, chatID)
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		logger.ErrorContext(ctx, "vm create tool: assign failed", "vm_id", vm.ID, "error", err)
		return result, nil
	}

	result.OK = true
	result.Payload = map[string]any{
		"vm_id":    assigned.ID,
		"status":   string(assigned.Status),
		"provider": string(assigned.Provider),
	}
	logger.InfoContext(ctx, "vm create tool: created and assigned", "vm_id", assigned.ID, "chat_id", chatID)
	return result, nil
}

var _ tool.Tool = (*VMCreateTool)(nil)

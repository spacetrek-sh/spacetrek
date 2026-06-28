package toolsvc

import (
	"context"
	"fmt"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// VMCreator is the subset of VM service needed by the create tool.
type VMCreator interface {
	ResolveEnvironment(ctx context.Context, envType string) (string, error)
	Create(ctx context.Context, envID, conversationID string, provider vmdomain.Provider, name string, workspaceSizeGB int, vcpu, memoryMB, diskMB, servicePort *int) (*vmdomain.VM, error)
	AssignToChat(ctx context.Context, vmID, chatID string) (*vmdomain.VM, error)
	GetByEnvironmentAndChatID(ctx context.Context, envID, chatID string) (*vmdomain.VM, error)
	HasSnapshot(ctx context.Context, vmID string) bool
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
				Description: "Environment type: 'uv' (Python/data science), 'bun' (JS/TS), or 'ubuntu' (generic shell). Pick the most specific match for the task.",
				Required:    true,
			},
			"service_port": {
				Type:        "integer",
				Description: "Port the VM's server listens on inside the VM. The VM is exposed publicly at https://<vm-name>.box.spacetrek.xyz, which forwards to this port. Defaults to 80. Set this to match the port your server binds (e.g. 8000 for uvicorn, 3000 for next dev, 5173 for vite).",
				Required:    false,
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

	// Guard: check if a VM with this environment already exists for this chat.
	existing, existingErr := t.creator.GetByEnvironmentAndChatID(ctx, envID, chatID)
	if existingErr == nil && existing != nil {
		switch {
		case existing.Status.IsActive():
			result.OK = false
			result.Error = fmt.Sprintf(
				"A VM with this environment is already active (id=%s, status=%s). "+
					"Use vm.execute_command with vm_id=%s directly.",
				existing.ID, existing.Status, existing.ID,
			)
			logger.DebugContext(ctx, "vm create tool: duplicate blocked, vm already active",
				"vm_id", existing.ID, "chat_id", chatID, "status", existing.Status)
			return result, nil
		case !existing.Status.IsTerminal():
			hint := "Use vm.start"
			if t.creator.HasSnapshot(ctx, existing.ID) {
				hint = fmt.Sprintf("Use vm.start with vm_id=%s to restore its workspace", existing.ID)
			}
			result.OK = false
			result.Error = fmt.Sprintf(
				"A VM with this environment already exists for this chat (id=%s, status=%s). %s.",
				existing.ID, existing.Status, hint,
			)
			logger.DebugContext(ctx, "vm create tool: duplicate blocked, existing recoverable vm",
				"vm_id", existing.ID, "chat_id", chatID, "status", existing.Status)
			return result, nil
		}
	}

	var servicePort *int
	if port, found := readIntArg(call.Arguments, "service_port"); found {
		if port < 1 || port > 65535 {
			result.OK = false
			result.Error = fmt.Sprintf("service_port must be between 1 and 65535, got %d", port)
			return result, nil
		}
		servicePort = &port
	}

	vm, err := t.creator.Create(ctx, envID, chatID, vmdomain.ProviderFirecracker, "", 2, nil, nil, nil, servicePort)
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
		"vm_id":        assigned.ID,
		"name":         assigned.Name,
		"status":       string(assigned.Status),
		"environment":  envType,
		"service_port": assigned.ServicePort,
		"public_url":   publicURL(assigned),
	}
	logger.InfoContext(ctx, "vm create tool: created and assigned", "vm_id", assigned.ID, "chat_id", chatID, "env_type", envType)
	return result, nil
}

var _ tool.Tool = (*VMCreateTool)(nil)

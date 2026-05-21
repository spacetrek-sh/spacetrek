package toolsvc

import (
	"context"
	"strings"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
)

// VMCommandExecutor is the subset of VM service needed by the command tool.
type VMCommandExecutor interface {
	ExecuteCommand(ctx context.Context, id, command string) (string, error)
}

// VMCommandTool executes shell commands on a target VM runtime.
type VMCommandTool struct {
	exec VMCommandExecutor
}

func NewVMCommandTool(exec VMCommandExecutor) *VMCommandTool {
	return &VMCommandTool{exec: exec}
}

func (t *VMCommandTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "vm.execute_command",
		Description: "Execute a shell command inside a VM runtime",
		Parameters: map[string]tool.Parameter{
			"vm_id": {
				Type:        "string",
				Description: "Target VM identifier",
				Required:    true,
			},
			"command": {
				Type:        "string",
				Description: "Shell command to execute",
				Required:    true,
			},
		},
	}
}

func (t *VMCommandTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	logger := pkglog.FromContext(ctx)
	result := tool.Result{ToolCallID: call.ID, ToolName: call.Name}
	if t.exec == nil {
		result.OK = false
		result.Error = "vm command executor is not configured"
		return result, nil
	}

	vmID, ok := readStringArg(call.Arguments, "vm_id")
	if !ok {
		result.OK = false
		result.Error = "missing required argument vm_id"
		return result, nil
	}

	command, ok := readStringArg(call.Arguments, "command")
	if !ok {
		result.OK = false
		result.Error = "missing required argument command"
		return result, nil
	}

	output, err := t.exec.ExecuteCommand(ctx, vmID, command)
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		if output != "" {
			result.Payload = map[string]any{"output": strings.TrimSpace(output)}
		}
		logger.ErrorContext(ctx, "vm command tool failed", "vm_id", vmID, "error", err)
		return result, nil
	}

	result.OK = true
	result.Payload = map[string]any{"output": strings.TrimSpace(output)}
	logger.DebugContext(ctx, "vm command tool executed", "vm_id", vmID, "output_len", len(output))
	return result, nil
}

func readStringArg(args map[string]any, key string) (string, bool) {
	if args == nil {
		return "", false
	}
	value, exists := args[key]
	if !exists {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	return text, true
}

func readIntArg(args map[string]any, key string) (int, bool) {
	if args == nil {
		return 0, false
	}
	value, exists := args[key]
	if !exists {
		return 0, false
	}
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}

func readBoolArg(args map[string]any, key string) (bool, bool) {
	if args == nil {
		return false, false
	}
	value, exists := args[key]
	if !exists {
		return false, false
	}
	b, ok := value.(bool)
	return b, ok
}

var _ tool.Tool = (*VMCommandTool)(nil)

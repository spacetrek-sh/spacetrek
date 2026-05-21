package toolsvc

import (
	"context"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
)

// VMFileWriter is the subset of VM service needed by the write file tool.
type VMFileWriter interface {
	WriteFile(ctx context.Context, id, path, content string, mode int) error
}

// VMWriteFileTool writes files to a target VM runtime.
type VMWriteFileTool struct {
	writer VMFileWriter
}

func NewVMWriteFileTool(writer VMFileWriter) *VMWriteFileTool {
	return &VMWriteFileTool{writer: writer}
}

func (t *VMWriteFileTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "vm.write_file",
		Description: "Write content to a file in a VM runtime, creating it if needed",
		Parameters: map[string]tool.Parameter{
			"vm_id": {
				Type:        "string",
				Description: "Target VM identifier",
				Required:    true,
			},
			"path": {
				Type:        "string",
				Description: "Absolute path to the file to write",
				Required:    true,
			},
			"content": {
				Type:        "string",
				Description: "File content to write",
				Required:    true,
			},
			"mode": {
				Type:        "integer",
				Description: "File permission mode as decimal (e.g. 493 for 0755). Default: 420 (0644)",
				Required:    false,
			},
		},
	}
}

func (t *VMWriteFileTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	logger := pkglog.FromContext(ctx)
	result := tool.Result{ToolCallID: call.ID, ToolName: call.Name}
	if t.writer == nil {
		result.OK = false
		result.Error = "vm file writer is not configured"
		return result, nil
	}

	vmID, ok := readStringArg(call.Arguments, "vm_id")
	if !ok {
		result.OK = false
		result.Error = "missing required argument vm_id"
		return result, nil
	}

	path, ok := readStringArg(call.Arguments, "path")
	if !ok {
		result.OK = false
		result.Error = "missing required argument path"
		return result, nil
	}

	content, ok := readStringArg(call.Arguments, "content")
	if !ok {
		result.OK = false
		result.Error = "missing required argument content"
		return result, nil
	}

	mode, _ := readIntArg(call.Arguments, "mode")

	if err := t.writer.WriteFile(ctx, vmID, path, content, mode); err != nil {
		result.OK = false
		result.Error = err.Error()
		logger.ErrorContext(ctx, "vm write file tool failed", "vm_id", vmID, "path", path, "error", err)
		return result, nil
	}

	result.OK = true
	logger.DebugContext(ctx, "vm write file tool executed", "vm_id", vmID, "path", path)
	return result, nil
}

var _ tool.Tool = (*VMWriteFileTool)(nil)

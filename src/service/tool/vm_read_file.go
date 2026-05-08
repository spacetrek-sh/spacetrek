package toolsvc

import (
	"context"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
)

// VMFileReader is the subset of VM service needed by the read file tool.
type VMFileReader interface {
	ReadFile(ctx context.Context, id, path string, offset, limit int) (string, error)
}

// VMReadFileTool reads files from a target VM runtime.
type VMReadFileTool struct {
	reader VMFileReader
}

func NewVMReadFileTool(reader VMFileReader) *VMReadFileTool {
	return &VMReadFileTool{reader: reader}
}

func (t *VMReadFileTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "vm.read_file",
		Description: "Read a file from a VM runtime, returning content with line numbers",
		Parameters: map[string]tool.Parameter{
			"vm_id": {
				Type:        "string",
				Description: "Target VM identifier",
				Required:    true,
			},
			"path": {
				Type:        "string",
				Description: "Absolute path to the file to read",
				Required:    true,
			},
			"offset": {
				Type:        "integer",
				Description: "1-based line number to start reading from (0 = from beginning)",
				Required:    false,
			},
			"limit": {
				Type:        "integer",
				Description: "Maximum number of lines to return (0 = all)",
				Required:    false,
			},
		},
	}
}

func (t *VMReadFileTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	logger := pkglog.FromContext(ctx)
	result := tool.Result{ToolCallID: call.ID, ToolName: call.Name}
	if t.reader == nil {
		result.OK = false
		result.Error = "vm file reader is not configured"
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

	offset, _ := readIntArg(call.Arguments, "offset")
	limit, _ := readIntArg(call.Arguments, "limit")

	content, err := t.reader.ReadFile(ctx, vmID, path, offset, limit)
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		if content != "" {
			result.Payload = map[string]any{"content": content}
		}
		logger.ErrorContext(ctx, "vm read file tool failed", "vm_id", vmID, "path", path, "error", err)
		return result, nil
	}

	result.OK = true
	result.Payload = map[string]any{"content": content}
	logger.DebugContext(ctx, "vm read file tool executed", "vm_id", vmID, "path", path, "content_len", len(content))
	return result, nil
}

var _ tool.Tool = (*VMReadFileTool)(nil)

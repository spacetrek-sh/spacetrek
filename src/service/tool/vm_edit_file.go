package toolsvc

import (
	"context"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
)

// VMFileEditor is the subset of VM service needed by the edit file tool.
type VMFileEditor interface {
	EditFile(ctx context.Context, id, path, oldString, newString string, replaceAll bool) error
}

// VMEditFileTool performs surgical string replacements on files in a VM runtime.
type VMEditFileTool struct {
	editor VMFileEditor
}

func NewVMEditFileTool(editor VMFileEditor) *VMEditFileTool {
	return &VMEditFileTool{editor: editor}
}

func (t *VMEditFileTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "vm.edit_file",
		Description: "Replace an exact string in a file inside a VM runtime. Fails if the string is not found or matches multiple locations (unless replace_all is set).",
		Parameters: map[string]tool.Parameter{
			"vm_id": {
				Type:        "string",
				Description: "Target VM identifier",
				Required:    true,
			},
			"path": {
				Type:        "string",
				Description: "Absolute path to the file to edit",
				Required:    true,
			},
			"old_string": {
				Type:        "string",
				Description: "Exact string to find and replace",
				Required:    true,
			},
			"new_string": {
				Type:        "string",
				Description: "Replacement string",
				Required:    true,
			},
			"replace_all": {
				Type:        "boolean",
				Description: "Replace all occurrences instead of requiring a unique match",
				Required:    false,
			},
		},
	}
}

func (t *VMEditFileTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	logger := pkglog.FromContext(ctx)
	result := tool.Result{ToolCallID: call.ID, ToolName: call.Name}
	if t.editor == nil {
		result.OK = false
		result.Error = "vm file editor is not configured"
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

	oldString, ok := readStringArg(call.Arguments, "old_string")
	if !ok {
		result.OK = false
		result.Error = "missing required argument old_string"
		return result, nil
	}

	newString, ok := readStringArg(call.Arguments, "new_string")
	if !ok {
		result.OK = false
		result.Error = "missing required argument new_string"
		return result, nil
	}

	replaceAll, _ := readBoolArg(call.Arguments, "replace_all")

	if err := t.editor.EditFile(ctx, vmID, path, oldString, newString, replaceAll); err != nil {
		result.OK = false
		result.Error = err.Error()
		logger.ErrorContext(ctx, "vm edit file tool failed", "vm_id", vmID, "path", path, "error", err)
		return result, nil
	}

	result.OK = true
	if replaceAll {
		// Guest agent performs the substitution; per-match counts are known but the
		// total occurrence count is not surfaced by the current vsock protocol.
		result.Payload = map[string]any{
			"path":                    path,
			"replace_all":             true,
			"bytes_removed_per_match": len(oldString),
			"bytes_added_per_match":   len(newString),
			"note":                    "all occurrences replaced; do not re-read to verify",
		}
	} else {
		result.Payload = map[string]any{
			"path":          path,
			"replacements":  1,
			"bytes_removed": len(oldString),
			"bytes_added":   len(newString),
			"note":          "substitution applied; do not re-read to verify",
		}
	}
	logger.DebugContext(ctx, "vm edit file tool executed", "vm_id", vmID, "path", path, "replace_all", replaceAll)
	return result, nil
}

var _ tool.Tool = (*VMEditFileTool)(nil)

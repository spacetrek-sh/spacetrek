package toolsvc

import (
	"context"

	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	"github.com/kumori-sh/spacetrk/src/core/domain/snapshot"
	"github.com/kumori-sh/spacetrk/src/core/domain/tool"
)

// VMSnapshotter is the subset of VM service needed by the snapshot tool.
type VMSnapshotter interface {
	CreateSnapshot(ctx context.Context, vmID string) (*snapshot.Snapshot, error)
}

// VMSnapshotTool creates a snapshot of a running VM.
type VMSnapshotTool struct {
	snapshotter VMSnapshotter
}

func NewVMSnapshotTool(snapshotter VMSnapshotter) *VMSnapshotTool {
	return &VMSnapshotTool{snapshotter: snapshotter}
}

func (t *VMSnapshotTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "vm.snapshot",
		Description: "Create a snapshot of a running VM to preserve its filesystem and memory state. Use this before stopping a VM if you want to resume it later with its state intact.",
		Parameters: map[string]tool.Parameter{
			"vm_id": {
				Type:        "string",
				Description: "ID of the VM to snapshot",
				Required:    true,
			},
		},
	}
}

func (t *VMSnapshotTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	logger := pkglog.FromContext(ctx)
	result := tool.Result{ToolCallID: call.ID, ToolName: call.Name}

	vmID, ok := readStringArg(call.Arguments, "vm_id")
	if !ok {
		result.OK = false
		result.Error = "missing required argument vm_id"
		return result, nil
	}

	snap, err := t.snapshotter.CreateSnapshot(ctx, vmID)
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		logger.ErrorContext(ctx, "vm snapshot tool failed", "vm_id", vmID, "error", err)
		return result, nil
	}

	result.OK = true
	result.Payload = map[string]any{
		"snapshot_id": snap.ID,
		"vm_id":       snap.VMID,
		"size_bytes":  snap.SizeBytes,
		"created_at":  snap.CreatedAt.Format("2006-01-02T15:04:05Z"),
	}
	logger.InfoContext(ctx, "vm snapshot tool: snapshot created", "snapshot_id", snap.ID, "vm_id", vmID)
	return result, nil
}

var _ tool.Tool = (*VMSnapshotTool)(nil)

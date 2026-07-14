package toolsvc

import (
	"context"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/agent"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
)

// MemoryStore is the subset of the memory service the tools depend on.
// Defining it here keeps the tool layer testable without spinning up the
// service or repo.
type MemoryStore interface {
	Set(ctx context.Context, chatID, key, value string, ttlSeconds int) error
	Get(ctx context.Context, chatID, key string) (value string, found bool, err error)
	Delete(ctx context.Context, chatID, key string) error
	List(ctx context.Context, chatID string) ([]*agent.MemoryEntry, error)
}

// ── memory.set ─────────────────────────────────────────────────────────────

// MemorySetTool persists a value under a chat-scoped key. Survives VM
// snapshot/resume cycles within the same chat.
type MemorySetTool struct{ store MemoryStore }

func NewMemorySetTool(store MemoryStore) *MemorySetTool {
	return &MemorySetTool{store: store}
}

func (t *MemorySetTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "memory.set",
		Description: "Persist a small value (≤4KB) under a chat-scoped key. Survives VM snapshot/resume within the same chat. Use it to remember pointers, intentions, and partial plans across turns — not bulk data.",
		Parameters: map[string]tool.Parameter{
			"key": {
				Type:        "string",
				Required:    true,
				Description: "Storage key. Must match [a-z0-9_:-]{1,64}.",
			},
			"value": {
				Type:        "string",
				Required:    true,
				Description: "Value to store. Hard limit 4 KB; larger payloads belong in a VM file.",
			},
			"ttl_seconds": {
				Type:        "integer",
				Required:    false,
				Description: "Optional wall-clock TTL. 0 or omitted = no expiry. Max 86400 (24h).",
			},
		},
	}
}

func (t *MemorySetTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	logger := pkglog.FromContext(ctx)
	result := tool.Result{ToolCallID: call.ID, ToolName: call.Name}

	chatID, ok := tool.ChatIDFromContext(ctx)
	if !ok {
		result.OK = false
		result.Error = "chat_id not available in context"
		return result, nil
	}

	key, ok := readStringArg(call.Arguments, "key")
	if !ok {
		result.OK = false
		result.Error = "missing required argument key"
		return result, nil
	}
	value, ok := readStringArg(call.Arguments, "value")
	if !ok {
		result.OK = false
		result.Error = "missing required argument value"
		return result, nil
	}
	ttlSeconds, _ := readIntArg(call.Arguments, "ttl_seconds")

	if err := t.store.Set(ctx, chatID, key, value, ttlSeconds); err != nil {
		result.OK = false
		result.Error = err.Error()
		logger.ErrorContext(ctx, "memory set tool failed",
			"chat_id", chatID, "key", key, "error", err)
		return result, nil
	}

	result.OK = true
	result.Payload = map[string]any{
		"key":    key,
		"stored": true,
	}
	logger.DebugContext(ctx, "memory set tool executed",
		"chat_id", chatID, "key", key, "value_len", len(value), "ttl_seconds", ttlSeconds)
	return result, nil
}

// ── memory.get ─────────────────────────────────────────────────────────────

// MemoryGetTool reads a value. Missing keys return {"value": ""} — never error.
type MemoryGetTool struct{ store MemoryStore }

func NewMemoryGetTool(store MemoryStore) *MemoryGetTool {
	return &MemoryGetTool{store: store}
}

func (t *MemoryGetTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "memory.get",
		Description: "Read a value previously stored with memory.set. Returns an empty value when the key does not exist or has expired — never errors on missing keys.",
		Parameters: map[string]tool.Parameter{
			"key": {
				Type:        "string",
				Required:    true,
				Description: "Storage key previously written with memory.set.",
			},
		},
	}
}

func (t *MemoryGetTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	logger := pkglog.FromContext(ctx)
	result := tool.Result{ToolCallID: call.ID, ToolName: call.Name}

	chatID, ok := tool.ChatIDFromContext(ctx)
	if !ok {
		result.OK = false
		result.Error = "chat_id not available in context"
		return result, nil
	}

	key, ok := readStringArg(call.Arguments, "key")
	if !ok {
		result.OK = false
		result.Error = "missing required argument key"
		return result, nil
	}

	value, found, err := t.store.Get(ctx, chatID, key)
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		logger.ErrorContext(ctx, "memory get tool failed",
			"chat_id", chatID, "key", key, "error", err)
		return result, nil
	}

	result.OK = true
	result.Payload = map[string]any{
		"key":   key,
		"value": value,
		"found": found,
	}
	logger.DebugContext(ctx, "memory get tool executed",
		"chat_id", chatID, "key", key, "found", found, "value_len", len(value))
	return result, nil
}

// ── memory.delete ──────────────────────────────────────────────────────────

// MemoryDeleteTool removes a key. Missing keys surface as an error so the
// LLM can distinguish delete-no-op from delete-succeeded.
type MemoryDeleteTool struct{ store MemoryStore }

func NewMemoryDeleteTool(store MemoryStore) *MemoryDeleteTool {
	return &MemoryDeleteTool{store: store}
}

func (t *MemoryDeleteTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "memory.delete",
		Description: "Delete a chat-scoped key previously written with memory.set. Errors if the key does not exist (or has expired).",
		Parameters: map[string]tool.Parameter{
			"key": {
				Type:        "string",
				Required:    true,
				Description: "Storage key to remove.",
			},
		},
	}
}

func (t *MemoryDeleteTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	logger := pkglog.FromContext(ctx)
	result := tool.Result{ToolCallID: call.ID, ToolName: call.Name}

	chatID, ok := tool.ChatIDFromContext(ctx)
	if !ok {
		result.OK = false
		result.Error = "chat_id not available in context"
		return result, nil
	}

	key, ok := readStringArg(call.Arguments, "key")
	if !ok {
		result.OK = false
		result.Error = "missing required argument key"
		return result, nil
	}

	if err := t.store.Delete(ctx, chatID, key); err != nil {
		result.OK = false
		result.Error = err.Error()
		logger.DebugContext(ctx, "memory delete tool failed",
			"chat_id", chatID, "key", key, "error", err)
		return result, nil
	}

	result.OK = true
	result.Payload = map[string]any{
		"key":     key,
		"deleted": true,
	}
	logger.DebugContext(ctx, "memory delete tool executed",
		"chat_id", chatID, "key", key)
	return result, nil
}

// ── memory.list ────────────────────────────────────────────────────────────

// MemoryListTool returns all non-expired entries for the current chat.
type MemoryListTool struct{ store MemoryStore }

func NewMemoryListTool(store MemoryStore) *MemoryListTool {
	return &MemoryListTool{store: store}
}

func (t *MemoryListTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "memory.list",
		Description: "List every key/value pair currently stored for this chat. Useful before re-planning a multi-step task to recall what was already observed.",
		Parameters:  map[string]tool.Parameter{},
	}
}

func (t *MemoryListTool) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	logger := pkglog.FromContext(ctx)
	result := tool.Result{ToolCallID: call.ID, ToolName: call.Name}

	chatID, ok := tool.ChatIDFromContext(ctx)
	if !ok {
		result.OK = false
		result.Error = "chat_id not available in context"
		return result, nil
	}

	entries, err := t.store.List(ctx, chatID)
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		logger.ErrorContext(ctx, "memory list tool failed",
			"chat_id", chatID, "error", err)
		return result, nil
	}

	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		item := map[string]any{
			"key":   entry.Key,
			"value": entry.Value,
		}
		if !entry.ExpiresAt.IsZero() {
			item["expires_at"] = entry.ExpiresAt.Format("2006-01-02T15:04:05Z")
		}
		items = append(items, item)
	}

	result.OK = true
	result.Payload = map[string]any{
		"count":   len(items),
		"entries": items,
	}
	logger.DebugContext(ctx, "memory list tool executed",
		"chat_id", chatID, "count", len(items))
	return result, nil
}

// Compile-time interface checks.
var (
	_ tool.Tool = (*MemorySetTool)(nil)
	_ tool.Tool = (*MemoryGetTool)(nil)
	_ tool.Tool = (*MemoryDeleteTool)(nil)
	_ tool.Tool = (*MemoryListTool)(nil)
)

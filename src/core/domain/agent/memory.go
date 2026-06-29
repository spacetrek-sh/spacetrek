package agent

import (
	"context"
	"time"
)

// MemoryEntry is a single chat-scoped key/value pair persisted across VM
// snapshot/resume cycles within the same chat. Values are small strings —
// anything bulkier belongs in the VM's /workspace, not here.
type MemoryEntry struct {
	ChatID    string
	Key       string
	Value     string
	ExpiresAt time.Time // zero value = no TTL
	CreatedAt time.Time
	UpdatedAt time.Time
}

// MemoryRepository defines the persistence contract for chat-scoped agent
// memory. Implementations are responsible for treating expired entries
// (ExpiresAt non-zero and in the past) as absent on read paths.
type MemoryRepository interface {
	Set(ctx context.Context, entry *MemoryEntry) error
	Get(ctx context.Context, chatID, key string) (*MemoryEntry, error)
	Delete(ctx context.Context, chatID, key string) error
	List(ctx context.Context, chatID string) ([]*MemoryEntry, error)
}

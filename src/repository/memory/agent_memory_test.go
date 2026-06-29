package memory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/agent"
)

func TestAgentMemory_SetGetRoundtrip(t *testing.T) {
	repo := NewAgentMemoryRepository()

	if err := repo.Set(context.Background(), &agent.MemoryEntry{
		ChatID: "chat-1",
		Key:    "intent",
		Value:  "deploy api",
	}); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := repo.Get(context.Background(), "chat-1", "intent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Value != "deploy api" {
		t.Fatalf("value = %q, want %q", got.Value, "deploy api")
	}
}

func TestAgentMemory_GetMissingReturnsNotFound(t *testing.T) {
	repo := NewAgentMemoryRepository()

	_, err := repo.Get(context.Background(), "chat-1", "nope")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var appErr *exception.AppError
	if !errors.As(err, &appErr) || appErr.Code != exception.CodeNotFound {
		t.Fatalf("expected NotFound error, got %v", err)
	}
}

func TestAgentMemory_TTLExpiry(t *testing.T) {
	repo := NewAgentMemoryRepository()

	if err := repo.Set(context.Background(), &agent.MemoryEntry{
		ChatID:    "chat-1",
		Key:       "ephemeral",
		Value:     "soon-gone",
		ExpiresAt: time.Now().Add(50 * time.Millisecond),
	}); err != nil {
		t.Fatalf("set: %v", err)
	}

	if _, err := repo.Get(context.Background(), "chat-1", "ephemeral"); err != nil {
		t.Fatalf("get before expiry should succeed, got: %v", err)
	}

	time.Sleep(80 * time.Millisecond)

	if _, err := repo.Get(context.Background(), "chat-1", "ephemeral"); err == nil {
		t.Fatal("get after expiry should fail")
	}
}

func TestAgentMemory_ListScopedToChat(t *testing.T) {
	repo := NewAgentMemoryRepository()

	for _, e := range []*agent.MemoryEntry{
		{ChatID: "chat-1", Key: "b", Value: "B"},
		{ChatID: "chat-1", Key: "a", Value: "A"},
		{ChatID: "chat-2", Key: "a", Value: "OTHER"},
	} {
		if err := repo.Set(context.Background(), e); err != nil {
			t.Fatalf("set: %v", err)
		}
	}

	got, err := repo.List(context.Background(), "chat-1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 entries for chat-1, got %d", len(got))
	}
	if got[0].Key != "a" || got[1].Key != "b" {
		t.Fatalf("expected sorted [a b], got [%s %s]", got[0].Key, got[1].Key)
	}
}

func TestAgentMemory_CrossChatIsolation(t *testing.T) {
	repo := NewAgentMemoryRepository()

	if err := repo.Set(context.Background(), &agent.MemoryEntry{
		ChatID: "chat-1", Key: "shared-key", Value: "from-chat-1",
	}); err != nil {
		t.Fatalf("set: %v", err)
	}

	_, err := repo.Get(context.Background(), "chat-2", "shared-key")
	if err == nil {
		t.Fatal("get from chat-2 should fail: cross-chat isolation broken")
	}
}

func TestAgentMemory_SetUpsertsExisting(t *testing.T) {
	repo := NewAgentMemoryRepository()

	if err := repo.Set(context.Background(), &agent.MemoryEntry{
		ChatID: "chat-1", Key: "k", Value: "v1",
	}); err != nil {
		t.Fatalf("set v1: %v", err)
	}
	if err := repo.Set(context.Background(), &agent.MemoryEntry{
		ChatID: "chat-1", Key: "k", Value: "v2",
	}); err != nil {
		t.Fatalf("set v2: %v", err)
	}

	got, err := repo.Get(context.Background(), "chat-1", "k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Value != "v2" {
		t.Fatalf("value = %q, want v2 (upsert)", got.Value)
	}
}

func TestAgentMemory_Delete(t *testing.T) {
	repo := NewAgentMemoryRepository()

	if err := repo.Set(context.Background(), &agent.MemoryEntry{
		ChatID: "chat-1", Key: "k", Value: "v",
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := repo.Delete(context.Background(), "chat-1", "k"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := repo.Get(context.Background(), "chat-1", "k"); err == nil {
		t.Fatal("get after delete should fail")
	}
}

func TestAgentMemory_DeleteMissingReturnsNotFound(t *testing.T) {
	repo := NewAgentMemoryRepository()

	err := repo.Delete(context.Background(), "chat-1", "ghost")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var appErr *exception.AppError
	if !errors.As(err, &appErr) || appErr.Code != exception.CodeNotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

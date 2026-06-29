package agentsvc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/agent"
)

type fakeMemoryRepo struct {
	setErr   error
	setCalls []*agent.MemoryEntry
	getEntry *agent.MemoryEntry
	getErr   error
	deleted  []string
	listOut  []*agent.MemoryEntry
	listErr  error
}

func (f *fakeMemoryRepo) Set(_ context.Context, e *agent.MemoryEntry) error {
	f.setCalls = append(f.setCalls, e)
	return f.setErr
}
func (f *fakeMemoryRepo) Get(_ context.Context, _, _ string) (*agent.MemoryEntry, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getEntry == nil {
		return nil, exception.NotFound("agent_memory", "")
	}
	return f.getEntry, nil
}
func (f *fakeMemoryRepo) Delete(_ context.Context, chatID, key string) error {
	f.deleted = append(f.deleted, chatID+"/"+key)
	return nil
}
func (f *fakeMemoryRepo) List(_ context.Context, _ string) ([]*agent.MemoryEntry, error) {
	return f.listOut, f.listErr
}

// ── ValidateMemoryKey ──────────────────────────────────────────────────────

func TestValidateMemoryKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"intent", true},
		{"user_id", true},
		{"env:prod", true},
		{"step-1", true},
		{"abc123", true},
		{strings.Repeat("a", 64), true},

		{"", false},             // empty
		{"Bad Key", false},      // spaces + uppercase
		{"UPPER", false},        // uppercase
		{"a.b", false},          // dot not in alphabet
		{"a/b", false},          // slash not in alphabet
		{strings.Repeat("a", 65), false}, // too long
	}
	for _, c := range cases {
		if got := ValidateMemoryKey(c.key); got != c.want {
			t.Errorf("ValidateMemoryKey(%q) = %v, want %v", c.key, got, c.want)
		}
	}
}

// ── Set validation ─────────────────────────────────────────────────────────

func TestMemoryService_Set_RejectsInvalidKey(t *testing.T) {
	repo := &fakeMemoryRepo{}
	svc := NewMemoryService(repo)

	err := svc.Set(context.Background(), "chat-1", "BAD KEY", "v", 0)
	if err == nil {
		t.Fatal("expected error")
	}
	var appErr *exception.AppError
	if !errors.As(err, &appErr) || appErr.Code != exception.CodeBadRequest {
		t.Fatalf("expected BadRequest, got %v", err)
	}
	if len(repo.setCalls) != 0 {
		t.Fatalf("repo.Set should not be called on invalid input, got %d calls", len(repo.setCalls))
	}
}

func TestMemoryService_Set_RejectsOversizedValue(t *testing.T) {
	repo := &fakeMemoryRepo{}
	svc := NewMemoryService(repo)

	big := strings.Repeat("a", memoryMaxValueBytes+1)
	err := svc.Set(context.Background(), "chat-1", "k", big, 0)
	if err == nil {
		t.Fatal("expected error")
	}
	var appErr *exception.AppError
	if !errors.As(err, &appErr) || appErr.Code != exception.CodeBadRequest {
		t.Fatalf("expected BadRequest, got %v", err)
	}
}

func TestMemoryService_Set_RejectsTTLOutOfBounds(t *testing.T) {
	repo := &fakeMemoryRepo{}
	svc := NewMemoryService(repo)

	for _, ttl := range []int{-1, memoryMaxTTLSeconds + 1} {
		err := svc.Set(context.Background(), "chat-1", "k", "v", ttl)
		if err == nil {
			t.Fatalf("ttl=%d: expected error", ttl)
		}
	}
}

func TestMemoryService_Set_AcceptsMaxValueAndTTL(t *testing.T) {
	repo := &fakeMemoryRepo{}
	svc := NewMemoryService(repo)

	maxValue := strings.Repeat("a", memoryMaxValueBytes)
	if err := svc.Set(context.Background(), "chat-1", "k", maxValue, memoryMaxTTLSeconds); err != nil {
		t.Fatalf("set: %v", err)
	}
	if len(repo.setCalls) != 1 {
		t.Fatalf("expected 1 set call, got %d", len(repo.setCalls))
	}
	if repo.setCalls[0].ExpiresAt.IsZero() {
		t.Fatalf("positive TTL should produce non-zero ExpiresAt")
	}
}

func TestMemoryService_Set_NoTTLMeansNoExpiry(t *testing.T) {
	repo := &fakeMemoryRepo{}
	svc := NewMemoryService(repo)

	if err := svc.Set(context.Background(), "chat-1", "k", "v", 0); err != nil {
		t.Fatalf("set: %v", err)
	}
	if !repo.setCalls[0].ExpiresAt.IsZero() {
		t.Fatalf("ttl=0 should produce zero ExpiresAt, got %v", repo.setCalls[0].ExpiresAt)
	}
}

// ── Get not-found handling ─────────────────────────────────────────────────

func TestMemoryService_Get_NotFoundReturnsEmpty(t *testing.T) {
	repo := &fakeMemoryRepo{}
	svc := NewMemoryService(repo)

	value, found, err := svc.Get(context.Background(), "chat-1", "ghost")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if found {
		t.Fatal("found should be false on missing key")
	}
	if value != "" {
		t.Fatalf("value = %q, want empty", value)
	}
}

func TestMemoryService_Get_PropagatesNonNotFound(t *testing.T) {
	repo := &fakeMemoryRepo{getErr: exception.Internal(errors.New("db down"))}
	svc := NewMemoryService(repo)

	_, _, err := svc.Get(context.Background(), "chat-1", "k")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestMemoryService_Get_ReturnsEntry(t *testing.T) {
	repo := &fakeMemoryRepo{getEntry: &agent.MemoryEntry{Value: "hello"}}
	svc := NewMemoryService(repo)

	value, found, err := svc.Get(context.Background(), "chat-1", "k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found || value != "hello" {
		t.Fatalf("value=%q found=%v, want hello/true", value, found)
	}
}

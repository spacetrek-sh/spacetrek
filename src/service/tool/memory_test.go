package toolsvc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/agent"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/tool"
)

// stubMemoryStore is a deterministic in-process MemoryStore for tool tests.
// Set/Get/Delete/List operate on a flat map keyed on chatID+key so cross-chat
// isolation failures surface naturally.
type stubMemoryStore struct {
	values   map[string]string
	expires  map[string]time.Time
	setErr   error
	getErr   error
	listErr  error
	ttlSeen  int
	lastTTL  int
	setCalls int
}

func newStubMemoryStore() *stubMemoryStore {
	return &stubMemoryStore{
		values:  make(map[string]string),
		expires: make(map[string]time.Time),
	}
}

func (s *stubMemoryStore) key(chatID, k string) string { return chatID + "\x00" + k }

func (s *stubMemoryStore) Set(_ context.Context, chatID, k, v string, ttl int) error {
	s.setCalls++
	s.lastTTL = ttl
	if s.setErr != nil {
		return s.setErr
	}
	s.values[s.key(chatID, k)] = v
	if ttl > 0 {
		s.ttlSeen++
		s.expires[s.key(chatID, k)] = time.Now().Add(time.Duration(ttl) * time.Second)
	}
	return nil
}

func (s *stubMemoryStore) Get(_ context.Context, chatID, k string) (string, bool, error) {
	if s.getErr != nil {
		return "", false, s.getErr
	}
	v, ok := s.values[s.key(chatID, k)]
	if !ok {
		return "", false, nil
	}
	if exp, hasExp := s.expires[s.key(chatID, k)]; hasExp && time.Now().After(exp) {
		return "", false, nil
	}
	return v, true, nil
}

func (s *stubMemoryStore) Delete(_ context.Context, chatID, k string) error {
	kk := s.key(chatID, k)
	if _, ok := s.values[kk]; !ok {
		return exception.NotFound("agent_memory", k)
	}
	delete(s.values, kk)
	delete(s.expires, kk)
	return nil
}

func (s *stubMemoryStore) List(_ context.Context, chatID string) ([]*agent.MemoryEntry, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	out := make([]*agent.MemoryEntry, 0)
	for key, v := range s.values {
		chat, k := splitStoreKey(key)
		if chat != chatID {
			continue
		}
		out = append(out, &agent.MemoryEntry{ChatID: chat, Key: k, Value: v})
	}
	return out, nil
}

func splitStoreKey(combined string) (chat, key string) {
	idx := strings.IndexByte(combined, 0)
	if idx < 0 {
		return combined, ""
	}
	return combined[:idx], combined[idx+1:]
}

func runTool(t *testing.T, tl tool.Tool, chatID string, args map[string]any) tool.Result {
	t.Helper()
	ctx := context.Background()
	if chatID != "" {
		ctx = tool.WithChatID(ctx, chatID)
	}
	res, err := tl.Execute(ctx, tool.Call{ID: "call-1", Name: tl.Definition().Name, Arguments: args})
	if err != nil {
		t.Fatalf("execute %s: %v", tl.Definition().Name, err)
	}
	return res
}

// ── memory.set ─────────────────────────────────────────────────────────────

func TestMemorySetTool_RoundtripViaGet(t *testing.T) {
	store := newStubMemoryStore()

	set := runTool(t, NewMemorySetTool(store), "chat-1", map[string]any{
		"key": "intent", "value": "deploy-api",
	})
	if !set.OK {
		t.Fatalf("set failed: %s", set.Error)
	}

	get := runTool(t, NewMemoryGetTool(store), "chat-1", map[string]any{
		"key": "intent",
	})
	if !get.OK {
		t.Fatalf("get failed: %s", get.Error)
	}
	payload, ok := get.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type: %T", get.Payload)
	}
	if payload["value"] != "deploy-api" {
		t.Fatalf("value = %v, want deploy-api", payload["value"])
	}
	if payload["found"] != true {
		t.Fatalf("found = %v, want true", payload["found"])
	}
}

func TestMemorySetTool_PassesTTL(t *testing.T) {
	store := newStubMemoryStore()
	res := runTool(t, NewMemorySetTool(store), "chat-1", map[string]any{
		"key": "k", "value": "v", "ttl_seconds": 60,
	})
	if !res.OK {
		t.Fatalf("set failed: %s", res.Error)
	}
	if store.lastTTL != 60 {
		t.Fatalf("ttl passed to store = %d, want 60", store.lastTTL)
	}
	if store.ttlSeen != 1 {
		t.Fatalf("expected one TTL-bearing set, got %d", store.ttlSeen)
	}
}

func TestMemorySetTool_RejectsInvalidKey(t *testing.T) {
	store := newStubMemoryStore()
	store.setErr = exception.BadRequest("invalid key")

	// The service layer enforces the regex; here we just verify the tool
	// surfaces the error verbatim instead of masking it as success.
	res := runTool(t, NewMemorySetTool(store), "chat-1", map[string]any{
		"key": "Bad Key!", "value": "v",
	})
	if res.OK {
		t.Fatal("expected failure on invalid key, got OK")
	}
}

func TestMemorySetTool_RequiresChatID(t *testing.T) {
	store := newStubMemoryStore()
	res, _ := NewMemorySetTool(store).Execute(context.Background(), tool.Call{
		ID: "c", Name: "memory.set", Arguments: map[string]any{"key": "k", "value": "v"},
	})
	if res.OK {
		t.Fatal("expected failure when chat_id is absent")
	}
	if res.Error == "" {
		t.Fatal("expected non-empty error")
	}
}

func TestMemorySetTool_MissingRequiredArgs(t *testing.T) {
	store := newStubMemoryStore()
	for _, args := range []map[string]any{
		{"value": "v"},
		{"key": "k"},
	} {
		res := runTool(t, NewMemorySetTool(store), "chat-1", args)
		if res.OK {
			t.Fatalf("expected failure with args %v, got OK", args)
		}
	}
}

// ── memory.get ─────────────────────────────────────────────────────────────

func TestMemoryGetTool_MissingKeyReturnsEmptyValue(t *testing.T) {
	store := newStubMemoryStore()
	res := runTool(t, NewMemoryGetTool(store), "chat-1", map[string]any{"key": "ghost"})
	if !res.OK {
		t.Fatalf("missing-key get should be OK, got error: %s", res.Error)
	}
	payload, _ := res.Payload.(map[string]any)
	if payload["value"] != "" {
		t.Fatalf("value = %q, want empty string", payload["value"])
	}
	if payload["found"] != false {
		t.Fatalf("found = %v, want false", payload["found"])
	}
}

func TestMemoryGetTool_CrossChatIsolation(t *testing.T) {
	store := newStubMemoryStore()
	if err := store.Set(context.Background(), "chat-A", "k", "secret-A", 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res := runTool(t, NewMemoryGetTool(store), "chat-B", map[string]any{"key": "k"})
	if !res.OK {
		t.Fatalf("get failed: %s", res.Error)
	}
	payload, _ := res.Payload.(map[string]any)
	if payload["value"] != "" {
		t.Fatalf("cross-chat leak: value = %v", payload["value"])
	}
	if payload["found"] != false {
		t.Fatalf("cross-chat leak: found = %v", payload["found"])
	}
}

// ── memory.delete ──────────────────────────────────────────────────────────

func TestMemoryDeleteTool_MissingKeySurfacesError(t *testing.T) {
	store := newStubMemoryStore()
	res := runTool(t, NewMemoryDeleteTool(store), "chat-1", map[string]any{"key": "ghost"})
	if res.OK {
		t.Fatal("expected failure on missing key, got OK")
	}
}

func TestMemoryDeleteTool_Success(t *testing.T) {
	store := newStubMemoryStore()
	if err := store.Set(context.Background(), "chat-1", "k", "v", 0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res := runTool(t, NewMemoryDeleteTool(store), "chat-1", map[string]any{"key": "k"})
	if !res.OK {
		t.Fatalf("delete failed: %s", res.Error)
	}
}

// ── memory.list ────────────────────────────────────────────────────────────

func TestMemoryListTool_ReturnsChatScopedEntries(t *testing.T) {
	store := newStubMemoryStore()
	if err := store.Set(context.Background(), "chat-1", "k1", "v1", 0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.Set(context.Background(), "chat-1", "k2", "v2", 0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.Set(context.Background(), "chat-2", "k1", "OTHER", 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	res := runTool(t, NewMemoryListTool(store), "chat-1", map[string]any{})
	if !res.OK {
		t.Fatalf("list failed: %s", res.Error)
	}
	payload, _ := res.Payload.(map[string]any)
	if payload["count"] != 2 {
		t.Fatalf("count = %v, want 2", payload["count"])
	}
}

// ── MemoryStore propagates real errors ─────────────────────────────────────

func TestMemoryGetTool_PropagatesRepoError(t *testing.T) {
	store := newStubMemoryStore()
	store.getErr = exception.Internal(errors.New("db down"))

	res := runTool(t, NewMemoryGetTool(store), "chat-1", map[string]any{"key": "k"})
	if res.OK {
		t.Fatal("expected failure, got OK")
	}
	if res.Error == "" {
		t.Fatal("expected error message")
	}
}

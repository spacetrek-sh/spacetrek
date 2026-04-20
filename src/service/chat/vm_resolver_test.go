package chatsvc

import (
	"context"
	"testing"

	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// fakeVMResolutionService mocks the VMResolutionService for testing.
type fakeVMResolutionService struct {
	getByChatIDFn             func(ctx context.Context, chatID string) (*vmdomain.VM, error)
	findPreviousLeaseForChatFn func(ctx context.Context, chatID string) (*vmdomain.VM, error)
	hasSnapshotFn             func(ctx context.Context, vmID string) bool
	resumeVMFn                func(ctx context.Context, vmID, chatID string) (*vmdomain.VM, error)
	assignToChatFn            func(ctx context.Context, vmID, chatID string) (*vmdomain.VM, error)
}

func (f *fakeVMResolutionService) GetByChatID(ctx context.Context, chatID string) (*vmdomain.VM, error) {
	if f.getByChatIDFn != nil {
		return f.getByChatIDFn(ctx, chatID)
	}
	return nil, nil
}

func (f *fakeVMResolutionService) FindPreviousLeaseForChat(ctx context.Context, chatID string) (*vmdomain.VM, error) {
	if f.findPreviousLeaseForChatFn != nil {
		return f.findPreviousLeaseForChatFn(ctx, chatID)
	}
	return nil, nil
}

func (f *fakeVMResolutionService) HasSnapshot(ctx context.Context, vmID string) bool {
	if f.hasSnapshotFn != nil {
		return f.hasSnapshotFn(ctx, vmID)
	}
	return false
}

func (f *fakeVMResolutionService) ResumeVM(ctx context.Context, vmID, chatID string) (*vmdomain.VM, error) {
	if f.resumeVMFn != nil {
		return f.resumeVMFn(ctx, vmID, chatID)
	}
	return &vmdomain.VM{ID: vmID, Status: vmdomain.StatusRunning}, nil
}

func (f *fakeVMResolutionService) AssignToChat(ctx context.Context, vmID, chatID string) (*vmdomain.VM, error) {
	if f.assignToChatFn != nil {
		return f.assignToChatFn(ctx, vmID, chatID)
	}
	return &vmdomain.VM{ID: vmID, Status: vmdomain.StatusRunning}, nil
}

func TestVMResolver_ActiveVMAssigned_ReturnsImmediately(t *testing.T) {
	fake := &fakeVMResolutionService{
		getByChatIDFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return &vmdomain.VM{ID: "vm-active", Status: vmdomain.StatusRunning}, nil
		},
	}

	resolver := NewVMResolver(fake)
	vmID, err := resolver.ResolveVMForChat(context.Background(), "chat-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vmID != "vm-active" {
		t.Fatalf("expected vm-active, got %q", vmID)
	}
}

func TestVMResolver_ActiveVMReady_ReturnsImmediately(t *testing.T) {
	fake := &fakeVMResolutionService{
		getByChatIDFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return &vmdomain.VM{ID: "vm-ready", Status: vmdomain.StatusReady}, nil
		},
	}

	resolver := NewVMResolver(fake)
	vmID, err := resolver.ResolveVMForChat(context.Background(), "chat-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vmID != "vm-ready" {
		t.Fatalf("expected vm-ready, got %q", vmID)
	}
}

func TestVMResolver_IdleVMWithNoPreviousLease_ReturnsEmpty(t *testing.T) {
	fake := &fakeVMResolutionService{
		getByChatIDFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return &vmdomain.VM{ID: "vm-idle", Status: vmdomain.StatusIdle}, nil
		},
		findPreviousLeaseForChatFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return nil, nil // no previous lease
		},
	}

	resolver := NewVMResolver(fake)
	vmID, err := resolver.ResolveVMForChat(context.Background(), "chat-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vmID != "" {
		t.Fatalf("expected empty vmID, got %q", vmID)
	}
}

func TestVMResolver_PreviousLeaseActive_Reassigns(t *testing.T) {
	fake := &fakeVMResolutionService{
		getByChatIDFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return nil, nil // no current assignment
		},
		findPreviousLeaseForChatFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return &vmdomain.VM{ID: "vm-prev", Status: vmdomain.StatusRunning}, nil
		},
		assignToChatFn: func(_ context.Context, vmID, chatID string) (*vmdomain.VM, error) {
			return &vmdomain.VM{ID: vmID, Status: vmdomain.StatusRunning}, nil
		},
	}

	resolver := NewVMResolver(fake)
	vmID, err := resolver.ResolveVMForChat(context.Background(), "chat-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vmID != "vm-prev" {
		t.Fatalf("expected vm-prev, got %q", vmID)
	}
}

func TestVMResolver_PreviousLeaseIdleWithSnapshot_Resumes(t *testing.T) {
	resumeCalled := false
	fake := &fakeVMResolutionService{
		getByChatIDFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return nil, nil
		},
		findPreviousLeaseForChatFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return &vmdomain.VM{ID: "vm-snap", Status: vmdomain.StatusIdle}, nil
		},
		hasSnapshotFn: func(_ context.Context, vmID string) bool {
			return vmID == "vm-snap"
		},
		resumeVMFn: func(_ context.Context, vmID, chatID string) (*vmdomain.VM, error) {
			resumeCalled = true
			return &vmdomain.VM{ID: vmID, Status: vmdomain.StatusRunning}, nil
		},
	}

	resolver := NewVMResolver(fake)
	vmID, err := resolver.ResolveVMForChat(context.Background(), "chat-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resumeCalled {
		t.Fatal("expected ResumeVM to be called")
	}
	if vmID != "vm-snap" {
		t.Fatalf("expected vm-snap, got %q", vmID)
	}
}

func TestVMResolver_PreviousLeaseIdleNoSnapshot_ReturnsEmpty(t *testing.T) {
	fake := &fakeVMResolutionService{
		getByChatIDFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return nil, nil
		},
		findPreviousLeaseForChatFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return &vmdomain.VM{ID: "vm-idle", Status: vmdomain.StatusIdle}, nil
		},
		hasSnapshotFn: func(_ context.Context, _ string) bool {
			return false
		},
	}

	resolver := NewVMResolver(fake)
	vmID, err := resolver.ResolveVMForChat(context.Background(), "chat-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vmID != "" {
		t.Fatalf("expected empty vmID (no snapshot), got %q", vmID)
	}
}

func TestVMResolver_PreviousLeaseTerminated_ReturnsEmpty(t *testing.T) {
	fake := &fakeVMResolutionService{
		getByChatIDFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return nil, nil
		},
		findPreviousLeaseForChatFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return &vmdomain.VM{ID: "vm-dead", Status: vmdomain.StatusTerminated}, nil
		},
	}

	resolver := NewVMResolver(fake)
	vmID, err := resolver.ResolveVMForChat(context.Background(), "chat-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vmID != "" {
		t.Fatalf("expected empty vmID (terminated), got %q", vmID)
	}
}

func TestVMResolver_ResumeFails_ReturnsEmptyGracefully(t *testing.T) {
	fake := &fakeVMResolutionService{
		getByChatIDFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return nil, nil
		},
		findPreviousLeaseForChatFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return &vmdomain.VM{ID: "vm-fail", Status: vmdomain.StatusIdle}, nil
		},
		hasSnapshotFn: func(_ context.Context, _ string) bool {
			return true
		},
		resumeVMFn: func(_ context.Context, _, _ string) (*vmdomain.VM, error) {
			return nil, errSnapRestoreFailed
		},
	}

	resolver := NewVMResolver(fake)
	vmID, err := resolver.ResolveVMForChat(context.Background(), "chat-1")
	if err != nil {
		t.Fatalf("expected no error (graceful fallback), got: %v", err)
	}
	if vmID != "" {
		t.Fatalf("expected empty vmID on resume failure, got %q", vmID)
	}
}

func TestVMResolver_AssignFails_ReturnsEmptyGracefully(t *testing.T) {
	fake := &fakeVMResolutionService{
		getByChatIDFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return nil, nil
		},
		findPreviousLeaseForChatFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return &vmdomain.VM{ID: "vm-assign-fail", Status: vmdomain.StatusRunning}, nil
		},
		assignToChatFn: func(_ context.Context, _, _ string) (*vmdomain.VM, error) {
			return nil, errAlreadyAssigned
		},
	}

	resolver := NewVMResolver(fake)
	vmID, err := resolver.ResolveVMForChat(context.Background(), "chat-1")
	if err != nil {
		t.Fatalf("expected no error (graceful fallback), got: %v", err)
	}
	if vmID != "" {
		t.Fatalf("expected empty vmID on assign failure, got %q", vmID)
	}
}

func TestVMResolver_GetByChatIDError_ProceedsToPreviousLease(t *testing.T) {
	fake := &fakeVMResolutionService{
		getByChatIDFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return nil, errNotFound
		},
		findPreviousLeaseForChatFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
			return &vmdomain.VM{ID: "vm-prev", Status: vmdomain.StatusRunning}, nil
		},
		assignToChatFn: func(_ context.Context, vmID, _ string) (*vmdomain.VM, error) {
			return &vmdomain.VM{ID: vmID, Status: vmdomain.StatusRunning}, nil
		},
	}

	resolver := NewVMResolver(fake)
	vmID, err := resolver.ResolveVMForChat(context.Background(), "chat-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vmID != "vm-prev" {
		t.Fatalf("expected vm-prev (from previous lease), got %q", vmID)
	}
}

// --- sentinel errors for fake ---

var errNotFound = &testError{"not found"}
var errSnapRestoreFailed = &testError{"snapshot restore failed"}
var errAlreadyAssigned = &testError{"already assigned to another chat"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

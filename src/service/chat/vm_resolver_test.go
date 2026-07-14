package chatsvc

import (
	"context"
	"testing"

	"github.com/spacetrek-sh/spacetrek/src/core/domain/environment"
	"github.com/spacetrek-sh/spacetrek/src/core/ports"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// fakeVMCollectionService mocks VMCollectionService for testing.
type fakeVMCollectionService struct {
	getByChatIDFn              func(ctx context.Context, chatID string) (*vmdomain.VM, error)
	listPreviousLeasesForChatFn func(ctx context.Context, chatID string) ([]*vmdomain.VM, error)
	hasSnapshotFn              func(ctx context.Context, vmID string) bool
}

func (f *fakeVMCollectionService) GetByChatID(ctx context.Context, chatID string) (*vmdomain.VM, error) {
	if f.getByChatIDFn != nil {
		return f.getByChatIDFn(ctx, chatID)
	}
	return nil, nil
}

func (f *fakeVMCollectionService) ListPreviousLeasesForChat(ctx context.Context, chatID string) ([]*vmdomain.VM, error) {
	if f.listPreviousLeasesForChatFn != nil {
		return f.listPreviousLeasesForChatFn(ctx, chatID)
	}
	return nil, nil
}

func (f *fakeVMCollectionService) HasSnapshot(ctx context.Context, vmID string) bool {
	if f.hasSnapshotFn != nil {
		return f.hasSnapshotFn(ctx, vmID)
	}
	return false
}

// fakeEnvRepo mocks environment.Repository for testing.
type fakeEnvRepo struct {
	envs map[string]*environment.Environment
}

func (f *fakeEnvRepo) Create(_ context.Context, _ *environment.Environment) error { return nil }
func (f *fakeEnvRepo) Delete(_ context.Context, _ string) error                   { return nil }
func (f *fakeEnvRepo) Update(_ context.Context, _ *environment.Environment) error { return nil }
func (f *fakeEnvRepo) List(_ context.Context) ([]*environment.Environment, error) { return nil, nil }
func (f *fakeEnvRepo) GetByID(_ context.Context, id string) (*environment.Environment, error) {
	if e, ok := f.envs[id]; ok {
		return e, nil
	}
	return nil, &testError{"not found"}
}

func TestCollector_NoVMs_ReturnsNil(t *testing.T) {
	collector := NewAvailableVMCollector(
		&fakeVMCollectionService{},
		&fakeEnvRepo{envs: map[string]*environment.Environment{}},
	)
	vms, err := collector.CollectAvailableVMs(context.Background(), "chat-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if vms != nil {
		t.Fatalf("expected nil, got %v", vms)
	}
}

func TestCollector_ActiveVM_IncludedWithEnvironment(t *testing.T) {
	envID := "env-uv"
	collector := NewAvailableVMCollector(
		&fakeVMCollectionService{
			getByChatIDFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
				return &vmdomain.VM{ID: "vm-active", EnvironmentID: envID, Status: vmdomain.StatusRunning}, nil
			},
		},
		&fakeEnvRepo{envs: map[string]*environment.Environment{
			envID: {ID: envID, Type: environment.Type("uv"), Description: "Python with uv"},
		}},
	)
	vms, err := collector.CollectAvailableVMs(context.Background(), "chat-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vms) != 1 {
		t.Fatalf("expected 1 VM, got %d", len(vms))
	}
	if vms[0].VMID != "vm-active" {
		t.Fatalf("expected vm-active, got %q", vms[0].VMID)
	}
	if vms[0].Environment != "uv" {
		t.Fatalf("expected uv, got %q", vms[0].Environment)
	}
	if vms[0].EnvDescription != "Python with uv" {
		t.Fatalf("expected Python with uv, got %q", vms[0].EnvDescription)
	}
}

func TestCollector_PreviousLeases_IncludedWithSnapshot(t *testing.T) {
	collector := NewAvailableVMCollector(
		&fakeVMCollectionService{
			listPreviousLeasesForChatFn: func(_ context.Context, _ string) ([]*vmdomain.VM, error) {
				return []*vmdomain.VM{
					{ID: "vm-1", EnvironmentID: "env-uv", Status: vmdomain.StatusTerminated},
					{ID: "vm-2", EnvironmentID: "env-bun", Status: vmdomain.StatusIdle},
				}, nil
			},
			hasSnapshotFn: func(_ context.Context, vmID string) bool {
				return vmID == "vm-1"
			},
		},
		&fakeEnvRepo{envs: map[string]*environment.Environment{
			"env-uv":  {ID: "env-uv", Type: environment.Type("uv"), Description: "Python env"},
			"env-bun": {ID: "env-bun", Type: environment.Type("bun"), Description: "Bun env"},
		}},
	)
	vms, err := collector.CollectAvailableVMs(context.Background(), "chat-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vms) != 2 {
		t.Fatalf("expected 2 VMs, got %d", len(vms))
	}

	// vm-1 has snapshot
	if vms[0].VMID != "vm-1" || !vms[0].HasSnapshot {
		t.Fatalf("expected vm-1 with snapshot, got %q hasSnapshot=%v", vms[0].VMID, vms[0].HasSnapshot)
	}
	// vm-2 no snapshot
	if vms[1].VMID != "vm-2" || vms[1].HasSnapshot {
		t.Fatalf("expected vm-2 without snapshot, got %q hasSnapshot=%v", vms[1].VMID, vms[1].HasSnapshot)
	}
}

func TestCollector_DeduplicatesActiveAndPrevious(t *testing.T) {
	collector := NewAvailableVMCollector(
		&fakeVMCollectionService{
			getByChatIDFn: func(_ context.Context, _ string) (*vmdomain.VM, error) {
				return &vmdomain.VM{ID: "vm-1", EnvironmentID: "env-uv", Status: vmdomain.StatusRunning}, nil
			},
			listPreviousLeasesForChatFn: func(_ context.Context, _ string) ([]*vmdomain.VM, error) {
				return []*vmdomain.VM{
					{ID: "vm-1", EnvironmentID: "env-uv", Status: vmdomain.StatusRunning}, // duplicate
					{ID: "vm-2", EnvironmentID: "env-bun", Status: vmdomain.StatusIdle},
				}, nil
			},
		},
		&fakeEnvRepo{envs: map[string]*environment.Environment{
			"env-uv":  {ID: "env-uv", Type: environment.Type("uv"), Description: "Python"},
			"env-bun": {ID: "env-bun", Type: environment.Type("bun"), Description: "Bun"},
		}},
	)
	vms, err := collector.CollectAvailableVMs(context.Background(), "chat-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vms) != 2 {
		t.Fatalf("expected 2 deduplicated VMs, got %d", len(vms))
	}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// Verify the collector implements AvailableVMCollector.
var _ AvailableVMCollector = (*vmCollector)(nil)

// Verify AvailableVM is in ports package (compile-time check).
var _ ports.AvailableVM

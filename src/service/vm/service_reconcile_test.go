package vm

import (
	"context"
	"errors"
	"testing"
	"time"

	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// reconcileBackend is a configurable vmdomain.Backend stub for reconciler
// tests. If statusErr is non-nil it is returned from Status; otherwise status
// is returned. Calls counts Status invocations so tests can assert the
// reconciler skipped a VM.
type reconcileBackend struct {
	statusErr error
	status    vmdomain.RuntimeStatus
	calls     int
}

func (b *reconcileBackend) Create(context.Context, vmdomain.CreateSpec) (string, error) {
	panic("not used")
}
func (b *reconcileBackend) Start(context.Context, string) error             { panic("not used") }
func (b *reconcileBackend) Stop(context.Context, string) error              { panic("not used") }
func (b *reconcileBackend) Destroy(context.Context, string) error           { panic("not used") }
func (b *reconcileBackend) StopPreserving(context.Context, string) error    { panic("not used") }
func (b *reconcileBackend) Status(_ context.Context, _ string) (vmdomain.RuntimeStatus, error) {
	b.calls++
	return b.status, b.statusErr
}
func (b *reconcileBackend) Execute(context.Context, string, []string) (string, string, int, error) {
	panic("not used")
}
func (b *reconcileBackend) GetMetrics(context.Context, string) (vmdomain.Metrics, error) {
	panic("not used")
}
func (b *reconcileBackend) CreateSnapshot(context.Context, string, vmdomain.SnapshotOptions) (*vmdomain.SnapshotResult, error) {
	panic("not used")
}
func (b *reconcileBackend) RestoreFromSnapshot(context.Context, vmdomain.CreateSpec, string) (string, error) {
	panic("not used")
}
func (b *reconcileBackend) ReadFile(context.Context, string, string, int, int) (string, error) {
	panic("not used")
}
func (b *reconcileBackend) WriteFile(context.Context, string, string, string, int) error {
	panic("not used")
}
func (b *reconcileBackend) EditFile(context.Context, string, string, string, string, bool) error {
	panic("not used")
}

// TestReconcile_NotFound_TransitionsToIdle is the regression test for
// restart-induced termination. Before Option B, the reconciler permanently
// terminated any VM whose Firecracker process was gone (e.g. after a
// spacetrek-api container restart) and released its IP — so the activator
// could never wake it again. After Option B, the VM transitions to idle and
// keeps its IP and lease so the activator can resume it on the next request.
func TestReconcile_NotFound_TransitionsToIdle(t *testing.T) {
	ctx := context.Background()
	repo := newResumeVMRepo()
	backend := &reconcileBackend{statusErr: errors.New("vm not found")}

	vm := vmdomain.New(vmdomain.CreateParams{EnvironmentID: "env-uv"})
	vm.Status = vmdomain.StatusRunning
	ip := "10.200.0.5"
	vm.IPAddress = &ip
	chatID := "chat-1"
	vm.ChatID = &chatID
	vm.ConversationID = chatID
	repo.vms[vm.ID] = vm
	repo.leases[vm.ID] = &vmdomain.Lease{ChatID: chatID, VMID: vm.ID, LeasedAt: time.Now().UTC()}

	svc := &Service{repo: repo, backend: backend}
	hook := &recordingHook{}
	svc.SetLifecycleHook(hook)

	svc.reconcileRuntimeStates(ctx)

	got, err := repo.GetByID(ctx, vm.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID after reconcile: vm=%v err=%v", got, err)
	}
	if got.Status != vmdomain.StatusIdle {
		t.Errorf("status: got %q, want %q", got.Status, vmdomain.StatusIdle)
	}
	if got.IPAddress == nil || *got.IPAddress != ip {
		t.Errorf("IPAddress: got %v, want %q (must be preserved across cold restart)", got.IPAddress, ip)
	}
	if got.ChatID != nil {
		t.Errorf("ChatID: got %q, want nil (Unassign clears it so FindPreviousLeaseForChat can match the row)",
			*got.ChatID)
	}
	if _, ok := repo.leases[vm.ID]; !ok {
		t.Errorf("lease: got released, want preserved (chat resume path needs it)")
	}

	var sawStatusChanged, sawUnassigned bool
	for _, evt := range hook.events {
		switch e := evt.(type) {
		case StatusChangedEvent:
			sawStatusChanged = true
			if e.Old() != vmdomain.StatusRunning {
				t.Errorf("StatusChanged.Old: got %q, want %q", e.Old(), vmdomain.StatusRunning)
			}
		case UnassignedEvent:
			sawUnassigned = true
			if e.Chat() != chatID {
				t.Errorf("Unassigned.Chat: got %q, want %q", e.Chat(), chatID)
			}
		}
	}
	if !sawStatusChanged {
		t.Errorf("expected StatusChangedEvent, got none")
	}
	if !sawUnassigned {
		t.Errorf("expected UnassignedEvent, got none")
	}
}

// TestReconcile_StoppedState_TransitionsToIdle covers the branch where
// Firecracker knows the VM but reports it as stopped. Same expectations as
// the not-found case: idle, IP kept, ChatID cleared for lease lookup.
func TestReconcile_StoppedState_TransitionsToIdle(t *testing.T) {
	ctx := context.Background()
	repo := newResumeVMRepo()
	backend := &reconcileBackend{
		status: vmdomain.RuntimeStatus{ID: "ignored", State: "stopped"},
	}

	vm := vmdomain.New(vmdomain.CreateParams{EnvironmentID: "env-uv"})
	vm.Status = vmdomain.StatusRunning
	ip := "10.200.0.6"
	vm.IPAddress = &ip
	chatID := "chat-2"
	vm.ChatID = &chatID
	repo.vms[vm.ID] = vm

	svc := &Service{repo: repo, backend: backend}
	svc.reconcileRuntimeStates(ctx)

	got, _ := repo.GetByID(ctx, vm.ID)
	if got.Status != vmdomain.StatusIdle {
		t.Errorf("status: got %q, want %q", got.Status, vmdomain.StatusIdle)
	}
	if got.IPAddress == nil || *got.IPAddress != ip {
		t.Errorf("IPAddress: got %v, want %q", got.IPAddress, ip)
	}
	if got.ChatID != nil {
		t.Errorf("ChatID: got %q, want nil", *got.ChatID)
	}
}

// TestReconcile_IdleVM_NotChecked asserts that the reconciler skips idle VMs
// rather than busy-pinging Firecracker for them every tick. Without this
// guard, every reconciler run (default 30s) would issue a doomed Status call
// for each cold VM.
func TestReconcile_IdleVM_NotChecked(t *testing.T) {
	ctx := context.Background()
	repo := newResumeVMRepo()
	backend := &reconcileBackend{statusErr: errors.New("should not be reached")}

	vm := vmdomain.New(vmdomain.CreateParams{EnvironmentID: "env-uv"})
	vm.Status = vmdomain.StatusIdle
	repo.vms[vm.ID] = vm

	svc := &Service{repo: repo, backend: backend}
	svc.reconcileRuntimeStates(ctx)

	if backend.calls != 0 {
		t.Errorf("backend.Status calls: got %d, want 0 (idle VMs must be skipped)", backend.calls)
	}
}

// TestReconcile_RunningVM_RefreshedMetadata is the regression guard: a
// running VM with a healthy Firecracker status keeps its status and gets its
// runtime metadata refreshed (PID, state) — it must not be transitioned to
// idle by the reconciler.
func TestReconcile_RunningVM_RefreshedMetadata(t *testing.T) {
	ctx := context.Background()
	repo := newResumeVMRepo()
	backend := &reconcileBackend{
		status: vmdomain.RuntimeStatus{ID: "ignored", State: "running", PID: 4242},
	}

	vm := vmdomain.New(vmdomain.CreateParams{EnvironmentID: "env-uv"})
	vm.Status = vmdomain.StatusRunning
	repo.vms[vm.ID] = vm

	svc := &Service{repo: repo, backend: backend}
	svc.reconcileRuntimeStates(ctx)

	got, _ := repo.GetByID(ctx, vm.ID)
	if got.Status != vmdomain.StatusRunning {
		t.Errorf("status: got %q, want %q (running VMs must not be touched)", got.Status, vmdomain.StatusRunning)
	}
	if got.PID == nil || *got.PID != 4242 {
		t.Errorf("PID: got %v, want 4242 (runtime metadata should be refreshed)", got.PID)
	}
}

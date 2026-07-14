package vm

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/spacetrek-sh/spacetrek/src/core/domain/snapshot"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// TestResumeVM_PersistsIPAddress is the regression test for the
// vm-dns-stale-after-restore bug. After ResumeVM, the VM's repo record
// must carry IPAddress so hostswriter.render keeps it in dnsmasq's
// addn-hosts (dnsmasq cannot serve a name without an IP).
//
// Fails on main: main's ResumeVM skips the IP branch entirely when the
// pre-restore repo record has IPAddress = nil, so the post-restore record
// stays nil and hostswriter drops the entry.
func TestResumeVM_PersistsIPAddress(t *testing.T) {
	ctx := context.Background()

	repo := newResumeVMRepo()
	ipAllocator, err := NewIPAllocator(repo, "10.200.0.2", "10.200.0.10")
	if err != nil {
		t.Fatalf("NewIPAllocator: %v", err)
	}

	// Place an idle VM with no IP — the bug scenario. ResumeVM must
	// allocate one rather than leaving the field empty.
	vm := vmdomain.New(vmdomain.CreateParams{EnvironmentID: "env-uv"})
	vm.Status = vmdomain.StatusIdle
	vm.ConversationID = "chat-1"
	vm.WorkspaceSizeGB = 1
	repo.vms[vm.ID] = vm

	// Place a full snapshot for the VM so ResumeVM takes the restore path.
	meta, _ := json.Marshal(snapshot.SnapshotMetadata{
		EnvironmentID: "env-uv",
		VCPU:          1,
		MemoryMB:      256,
	})
	snap := &snapshot.Snapshot{
		ID:           "snap-1",
		VMID:         vm.ID,
		Type:         snapshot.TypeFull,
		SnapshotPath: t.TempDir(),
		Metadata:     meta,
		CreatedAt:    time.Now().UTC(),
	}
	snapRepo := &resumeSnapRepo{snaps: map[string]*snapshot.Snapshot{snap.ID: snap}}

	backend := &resumeBackend{restoreID: "restored-runtime"}

	svc := &Service{
		repo:        repo,
		snapRepo:    snapRepo,
		backend:     backend,
		ipAllocator: ipAllocator,
	}

	if _, err := svc.ResumeVM(ctx, vm.ID, "chat-1"); err != nil {
		t.Fatalf("ResumeVM: %v", err)
	}

	restored, err := repo.GetByID(ctx, vm.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if restored.IPAddress == nil || *restored.IPAddress == "" {
		t.Fatalf("expected IPAddress to be persisted after restore, got nil")
	}

	// hostswriter.render eligibility: it filters out any VM that is
	// terminated, has no name, or has no IPAddress. The restored VM must
	// pass all three checks to land in dnsmasq's addn-hosts.
	if !hostswriterEligible(restored) {
		t.Errorf("restored VM is not hostswriter-eligible: status=%s name=%q ip=%v",
			restored.Status, restored.Name, restored.IPAddress)
	}

	allVMs, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("repo.List: %v", err)
	}
	var sawRestored bool
	for _, v := range allVMs {
		if v.ID == restored.ID && hostswriterEligible(v) {
			sawRestored = true
		}
	}
	if !sawRestored {
		t.Errorf("restored VM %q would be filtered out of hostswriter.render(repo.List())", restored.Name)
	}
}

// TestResumeVM_EmptyChatID_NoLease is the regression test for activator-driven
// resumes. The activator passes empty chatID because the wake didn't come from
// a chat turn — it just needs the VM running so traffic can be forwarded.
// ResumeVM must NOT call AssignToChatIfAvailable in this case, since the
// chat_id column is UUID-typed and any synthetic placeholder (the previous
// "ingress:"+id scheme) fails postgres validation. The VM must still land at
// status=running with a fresh IdleDeadlineAt and an allocated IP.
func TestResumeVM_EmptyChatID_NoLease(t *testing.T) {
	ctx := context.Background()

	repo := newResumeVMRepo()
	ipAllocator, err := NewIPAllocator(repo, "10.200.0.2", "10.200.0.10")
	if err != nil {
		t.Fatalf("NewIPAllocator: %v", err)
	}

	vm := vmdomain.New(vmdomain.CreateParams{EnvironmentID: "env-uv"})
	vm.Status = vmdomain.StatusIdle
	vm.WorkspaceSizeGB = 1
	repo.vms[vm.ID] = vm

	met, _ := json.Marshal(snapshot.SnapshotMetadata{
		EnvironmentID: "env-uv",
		VCPU:          1,
		MemoryMB:      256,
	})
	snap := &snapshot.Snapshot{
		ID:           "snap-empty",
		VMID:         vm.ID,
		Type:         snapshot.TypeFull,
		SnapshotPath: t.TempDir(),
		Metadata:     met,
		CreatedAt:    time.Now().UTC(),
	}
	snapRepo := &resumeSnapRepo{snaps: map[string]*snapshot.Snapshot{snap.ID: snap}}
	backend := &resumeBackend{restoreID: "restored-runtime"}

	svc := &Service{
		repo:        repo,
		snapRepo:    snapRepo,
		backend:     backend,
		ipAllocator: ipAllocator,
	}

	if _, err := svc.ResumeVM(ctx, vm.ID, ""); err != nil {
		t.Fatalf("ResumeVM with empty chatID: %v", err)
	}

	restored, err := repo.GetByID(ctx, vm.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if restored.Status != vmdomain.StatusRunning {
		t.Errorf("status: got %q, want %q", restored.Status, vmdomain.StatusRunning)
	}
	if restored.ChatID != nil {
		t.Errorf("ChatID: got %q, want nil (empty chatID must not create a binding)", *restored.ChatID)
	}
	if restored.IPAddress == nil || *restored.IPAddress == "" {
		t.Errorf("IPAddress: got nil, want an allocated IP")
	}
	if _, ok := repo.leases[vm.ID]; ok {
		t.Errorf("lease: got a record, want none (AssignToChatIfAvailable must be skipped on empty chatID)")
	}
}

// hostswriterEligible mirrors the eligibility filter in
// src/service/vm/hostswriter/writer.go. Inlined here because the vm
// package cannot import hostswriter (hostswriter's hook.go already
// imports vm — the cycle would fail to compile).
func hostswriterEligible(v *vmdomain.VM) bool {
	if v == nil || v.IsTerminated() {
		return false
	}
	if v.Name == "" {
		return false
	}
	if v.IPAddress == nil || *v.IPAddress == "" {
		return false
	}
	return true
}

// resumeVMRepo is a minimal vmdomain.Repository that supports the
// ResumeVM code path. Other methods panic so we notice drift.
type resumeVMRepo struct {
	vms    map[string]*vmdomain.VM
	leases map[string]*vmdomain.Lease
}

func newResumeVMRepo() *resumeVMRepo {
	return &resumeVMRepo{vms: map[string]*vmdomain.VM{}, leases: map[string]*vmdomain.Lease{}}
}

func (r *resumeVMRepo) clone(v *vmdomain.VM) *vmdomain.VM { cp := *v; return &cp }

func (r *resumeVMRepo) Create(_ context.Context, v *vmdomain.VM) error {
	r.vms[v.ID] = r.clone(v)
	return nil
}

func (r *resumeVMRepo) GetByID(_ context.Context, id string) (*vmdomain.VM, error) {
	v, ok := r.vms[id]
	if !ok {
		return nil, nil
	}
	return r.clone(v), nil
}

func (r *resumeVMRepo) GetByName(context.Context, string) (*vmdomain.VM, error) {
	panic("not used")
}

func (r *resumeVMRepo) Update(_ context.Context, v *vmdomain.VM) error {
	r.vms[v.ID] = r.clone(v)
	return nil
}

func (r *resumeVMRepo) Delete(_ context.Context, id string) error {
	delete(r.vms, id)
	return nil
}

func (r *resumeVMRepo) List(_ context.Context) ([]*vmdomain.VM, error) {
	out := make([]*vmdomain.VM, 0, len(r.vms))
	for _, v := range r.vms {
		out = append(out, r.clone(v))
	}
	return out, nil
}

func (r *resumeVMRepo) GetAvailablePool(context.Context, vmdomain.Provider, int) ([]*vmdomain.VM, error) {
	panic("not used")
}

func (r *resumeVMRepo) GetByEnvironmentID(context.Context, string) ([]*vmdomain.VM, error) {
	panic("not used")
}

func (r *resumeVMRepo) GetByChatID(context.Context, string) (*vmdomain.VM, error) {
	panic("not used")
}

func (r *resumeVMRepo) GetActiveVMs(_ context.Context) ([]*vmdomain.VM, error) {
	out := make([]*vmdomain.VM, 0, len(r.vms))
	for _, v := range r.vms {
		if v.Status == vmdomain.StatusTerminated {
			continue
		}
		out = append(out, r.clone(v))
	}
	return out, nil
}

func (r *resumeVMRepo) GetActiveByUserID(context.Context, string) ([]*vmdomain.VM, error) {
	panic("not used")
}

func (r *resumeVMRepo) GetAllByUserID(context.Context, string) ([]*vmdomain.VM, error) {
	panic("not used")
}

func (r *resumeVMRepo) GetByEnvironmentAndChatID(context.Context, string, string) (*vmdomain.VM, error) {
	panic("not used")
}

func (r *resumeVMRepo) AssignToChatIfAvailable(_ context.Context, vmID, chatID string, idleDeadlineAt *time.Time) (*vmdomain.VM, error) {
	v, ok := r.vms[vmID]
	if !ok {
		return nil, nil
	}
	v.AssignTo(chatID)
	v.IdleDeadlineAt = idleDeadlineAt
	r.leases[vmID] = &vmdomain.Lease{ChatID: chatID, VMID: vmID, LeasedAt: time.Now().UTC()}
	return r.clone(v), nil
}

func (r *resumeVMRepo) ReleaseActiveLeaseByVM(_ context.Context, vmID string) error {
	delete(r.leases, vmID)
	return nil
}

func (r *resumeVMRepo) ListActiveLeasesByChat(_ context.Context, chatID string) ([]vmdomain.Lease, error) {
	out := make([]vmdomain.Lease, 0)
	for _, l := range r.leases {
		if l.ChatID == chatID {
			out = append(out, *l)
		}
	}
	return out, nil
}

func (r *resumeVMRepo) FindPreviousLeaseForChat(context.Context, string) (*vmdomain.VM, error) {
	panic("not used")
}

func (r *resumeVMRepo) ListPreviousLeasesForChat(context.Context, string) ([]*vmdomain.VM, error) {
	panic("not used")
}

func (r *resumeVMRepo) GetAllocatedIPs(_ context.Context) ([]string, error) {
	out := make([]string, 0)
	for _, v := range r.vms {
		if v.IPAddress != nil && *v.IPAddress != "" && v.Status != vmdomain.StatusTerminated {
			out = append(out, *v.IPAddress)
		}
	}
	return out, nil
}

func (r *resumeVMRepo) GetAllocatedIPsExclude(_ context.Context, excludeVMID string) ([]string, error) {
	out := make([]string, 0)
	for id, v := range r.vms {
		if id == excludeVMID {
			continue
		}
		if v.IPAddress != nil && *v.IPAddress != "" && v.Status != vmdomain.StatusTerminated {
			out = append(out, *v.IPAddress)
		}
	}
	return out, nil
}

func (r *resumeVMRepo) SetIPAddress(_ context.Context, vmID, ip string) error {
	v, ok := r.vms[vmID]
	if !ok {
		return nil
	}
	v.IPAddress = &ip
	return nil
}

func (r *resumeVMRepo) ReleaseIPAddress(_ context.Context, vmID string) error {
	v, ok := r.vms[vmID]
	if !ok {
		return nil
	}
	v.IPAddress = nil
	return nil
}

// resumeSnapRepo is a minimal snapshot.Repository for the resume test.
type resumeSnapRepo struct {
	snaps map[string]*snapshot.Snapshot
}

func (r *resumeSnapRepo) Create(_ context.Context, s *snapshot.Snapshot) error {
	r.snaps[s.ID] = s
	return nil
}

func (r *resumeSnapRepo) GetByID(_ context.Context, id string) (*snapshot.Snapshot, error) {
	s, ok := r.snaps[id]
	if !ok {
		return nil, nil
	}
	return s, nil
}

func (r *resumeSnapRepo) GetByVMID(_ context.Context, vmID string) ([]*snapshot.Snapshot, error) {
	out := make([]*snapshot.Snapshot, 0)
	for _, s := range r.snaps {
		if s.VMID == vmID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r *resumeSnapRepo) GetLatestFull(context.Context, string) (*snapshot.Snapshot, error) {
	panic("not used")
}

func (r *resumeSnapRepo) GetLatestByVMID(_ context.Context, vmID string) (*snapshot.Snapshot, error) {
	var latest *snapshot.Snapshot
	for _, s := range r.snaps {
		if s.VMID != vmID {
			continue
		}
		if latest == nil || s.CreatedAt.After(latest.CreatedAt) {
			latest = s
		}
	}
	if latest == nil {
		return nil, nil
	}
	return latest, nil
}

func (r *resumeSnapRepo) Delete(_ context.Context, id string) error {
	delete(r.snaps, id)
	return nil
}

func (r *resumeSnapRepo) ListOrphaned(context.Context, time.Duration) ([]*snapshot.Snapshot, error) {
	panic("not used")
}

// resumeBackend is a minimal vmdomain.Backend for the resume test. It
// reports a running state with a live vsock so waitForExecuteReadiness
// returns immediately.
type resumeBackend struct {
	restoreID string
}

func (b *resumeBackend) Create(context.Context, vmdomain.CreateSpec) (string, error) {
	panic("not used")
}

func (b *resumeBackend) Start(context.Context, string) error       { panic("not used") }
func (b *resumeBackend) Stop(context.Context, string) error        { panic("not used") }
func (b *resumeBackend) Destroy(context.Context, string) error     { panic("not used") }
func (b *resumeBackend) StopPreserving(context.Context, string) error { panic("not used") }

func (b *resumeBackend) Status(_ context.Context, id string) (vmdomain.RuntimeStatus, error) {
	cid := uint32(3)
	return vmdomain.RuntimeStatus{
		ID:        id,
		State:     "running",
		PID:       1234,
		VsockPath: "/tmp/vsock",
		GuestCID:  cid,
	}, nil
}

func (b *resumeBackend) Execute(context.Context, string, []string) (string, string, int, error) {
	return "", "", 0, nil
}

func (b *resumeBackend) GetMetrics(context.Context, string) (vmdomain.Metrics, error) {
	panic("not used")
}

func (b *resumeBackend) CreateSnapshot(context.Context, string, vmdomain.SnapshotOptions) (*vmdomain.SnapshotResult, error) {
	panic("not used")
}

func (b *resumeBackend) RestoreFromSnapshot(_ context.Context, _ vmdomain.CreateSpec, _ string) (string, error) {
	return b.restoreID, nil
}

func (b *resumeBackend) ReadFile(context.Context, string, string, int, int) (string, error) {
	panic("not used")
}

func (b *resumeBackend) WriteFile(context.Context, string, string, string, int) error {
	panic("not used")
}

func (b *resumeBackend) EditFile(context.Context, string, string, string, string, bool) error {
	panic("not used")
}

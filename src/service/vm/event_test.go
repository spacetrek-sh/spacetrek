package vm

import (
	"context"
	"testing"
	"time"

	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// recordingHook captures every Event delivered to OnVMEvent in order.
// Tests assert on recorder.Events to verify the firing sequence and types.
type recordingHook struct {
	events []Event
}

func (r *recordingHook) OnVMEvent(_ context.Context, evt Event) {
	r.events = append(r.events, evt)
}

func (r *recordingHook) Types() []EventType {
	out := make([]EventType, len(r.events))
	for i, e := range r.events {
		out[i] = e.Type()
	}
	return out
}

// eventRepoStub is a hand-rolled Repository that supports the methods
// exercised by the event tests: GetByID, Update, AssignToChatIfAvailable,
// ReleaseActiveLeaseByVM. Every other method panics so we notice if the
// test drifts onto an unsupported code path.
type eventRepoStub struct {
	byID  map[string]*vmdomain.VM
	lease map[string]string // vmID -> chatID for active leases
}

func newEventRepoStub(vms ...*vmdomain.VM) *eventRepoStub {
	s := &eventRepoStub{
		byID:  make(map[string]*vmdomain.VM, len(vms)),
		lease: make(map[string]string),
	}
	for _, v := range vms {
		s.byID[v.ID] = v
	}
	return s
}

func (s *eventRepoStub) GetByID(_ context.Context, id string) (*vmdomain.VM, error) {
	if v, ok := s.byID[id]; ok {
		cp := *v
		return &cp, nil
	}
	return nil, nil
}

func (s *eventRepoStub) Update(_ context.Context, vm *vmdomain.VM) error {
	cp := *vm
	s.byID[vm.ID] = &cp
	return nil
}

func (s *eventRepoStub) AssignToChatIfAvailable(_ context.Context, vmID, chatID string, _ *time.Time) (*vmdomain.VM, error) {
	vm, ok := s.byID[vmID]
	if !ok {
		return nil, nil
	}
	vm.ChatID = &chatID
	vm.AssignedAt = ptrTime(time.Now().UTC())
	s.lease[vmID] = chatID
	cp := *vm
	return &cp, nil
}

func (s *eventRepoStub) ReleaseActiveLeaseByVM(_ context.Context, vmID string) error {
	delete(s.lease, vmID)
	if vm, ok := s.byID[vmID]; ok {
		vm.ChatID = nil
		vm.AssignedAt = nil
	}
	return nil
}

func ptrTime(t time.Time) *time.Time { return &t }

// Everything else panics — if the test drifts onto these, we want a loud signal.
func (s *eventRepoStub) Create(context.Context, *vmdomain.VM) error              { panic("not used") }
func (s *eventRepoStub) GetByName(context.Context, string) (*vmdomain.VM, error) { panic("not used") }
func (s *eventRepoStub) Delete(context.Context, string) error                    { panic("not used") }
func (s *eventRepoStub) List(context.Context) ([]*vmdomain.VM, error)            { panic("not used") }
func (s *eventRepoStub) GetAvailablePool(context.Context, vmdomain.Provider, int) ([]*vmdomain.VM, error) {
	panic("not used")
}
func (s *eventRepoStub) GetByEnvironmentID(context.Context, string) ([]*vmdomain.VM, error) {
	panic("not used")
}
func (s *eventRepoStub) GetByChatID(context.Context, string) (*vmdomain.VM, error) {
	panic("not used")
}
func (s *eventRepoStub) GetActiveVMs(context.Context) ([]*vmdomain.VM, error) { panic("not used") }
func (s *eventRepoStub) GetActiveByUserID(context.Context, string) ([]*vmdomain.VM, error) {
	panic("not used")
}
func (s *eventRepoStub) GetAllByUserID(context.Context, string) ([]*vmdomain.VM, error) {
	panic("not used")
}
func (s *eventRepoStub) GetByEnvironmentAndChatID(context.Context, string, string) (*vmdomain.VM, error) {
	panic("not used")
}
func (s *eventRepoStub) ListActiveLeasesByChat(context.Context, string) ([]vmdomain.Lease, error) {
	panic("not used")
}
func (s *eventRepoStub) FindPreviousLeaseForChat(context.Context, string) (*vmdomain.VM, error) {
	panic("not used")
}
func (s *eventRepoStub) ListPreviousLeasesForChat(context.Context, string) ([]*vmdomain.VM, error) {
	panic("not used")
}
func (s *eventRepoStub) GetAllocatedIPs(context.Context) ([]string, error) { panic("not used") }
func (s *eventRepoStub) GetAllocatedIPsExclude(context.Context, string) ([]string, error) {
	panic("not used")
}
func (s *eventRepoStub) SetIPAddress(context.Context, string, string) error { panic("not used") }
func (s *eventRepoStub) ReleaseIPAddress(context.Context, string) error     { panic("not used") }

// newVMForEventTests builds a VM with the requested status and chat binding.
func newVMForEventTests(id string, status vmdomain.Status, chatID string) *vmdomain.VM {
	vm := vmdomain.New(vmdomain.CreateParams{EnvironmentID: "env"})
	vm.ID = id
	vm.Name = "test-vm"
	vm.Status = status
	if chatID != "" {
		c := chatID
		vm.ChatID = &c
	}
	return vm
}

func TestAssignToChat_FiresAssignedEvent(t *testing.T) {
	vm := newVMForEventTests("vm-1", vmdomain.StatusReady, "")
	repo := newEventRepoStub(vm)
	recorder := &recordingHook{}
	s := &Service{repo: repo, hook: recorder, idleTimeout: time.Minute}

	ctx := context.Background()
	assigned, err := s.AssignToChat(ctx, "vm-1", "chat-1")
	if err != nil {
		t.Fatalf("AssignToChat: %v", err)
	}
	if assigned == nil {
		t.Fatal("expected assigned VM, got nil")
	}

	if got := recorder.Types(); len(got) != 1 || got[0] != EventAssigned {
		t.Errorf("expected [vm.assigned], got %v", got)
	}
	if ae, ok := recorder.events[0].(AssignedEvent); !ok || ae.Chat() != "chat-1" {
		t.Errorf("expected AssignedEvent chat=chat-1, got %+v", recorder.events[0])
	}
}

func TestUnassign_FiresUnassignedEvent(t *testing.T) {
	vm := newVMForEventTests("vm-1", vmdomain.StatusRunning, "chat-1")
	repo := newEventRepoStub(vm)
	recorder := &recordingHook{}
	s := &Service{repo: repo, hook: recorder, idleTimeout: time.Minute}

	if _, err := s.Unassign(context.Background(), "vm-1"); err != nil {
		t.Fatalf("Unassign: %v", err)
	}

	if got := recorder.Types(); len(got) != 1 || got[0] != EventUnassigned {
		t.Errorf("expected [vm.unassigned], got %v", got)
	}
	if ue, ok := recorder.events[0].(UnassignedEvent); !ok || ue.Chat() != "chat-1" {
		t.Errorf("expected UnassignedEvent chat=chat-1, got %+v", recorder.events[0])
	}
}

func TestReadPaths_FireZeroEvents(t *testing.T) {
	// Get is a pure read; it must not notify the hook.
	vm := newVMForEventTests("vm-1", vmdomain.StatusRunning, "chat-1")
	repo := newEventRepoStub(vm)
	recorder := &recordingHook{}
	s := &Service{repo: repo, hook: recorder, idleTimeout: time.Minute}

	if _, err := s.Get(context.Background(), "vm-1"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(recorder.events) != 0 {
		t.Errorf("expected zero events from Get, got %v", recorder.Types())
	}
}

func TestNoHook_ConfiguredIsSafe(t *testing.T) {
	// A Service without a hook must not panic when transitions fire.
	vm := newVMForEventTests("vm-1", vmdomain.StatusReady, "")
	repo := newEventRepoStub(vm)
	s := &Service{repo: repo, idleTimeout: time.Minute} // no hook

	if _, err := s.AssignToChat(context.Background(), "vm-1", "chat-1"); err != nil {
		t.Fatalf("AssignToChat without hook: %v", err)
	}
}

// Constructor sanity: each NewXxxEvent factory must populate the VMID/VM
// accessors so subscribers that filter on VMID don't nil-deref.
func TestEventConstructors(t *testing.T) {
	vm := newVMForEventTests("vm-1", vmdomain.StatusRunning, "chat-1")

	cases := []struct {
		name string
		evt  Event
		want EventType
	}{
		{"created", NewCreatedEvent(vm), EventCreated},
		{"status_changed", NewStatusChangedEvent(vm, vmdomain.StatusIdle), EventStatusChanged},
		{"assigned", NewAssignedEvent(vm, "chat-1"), EventAssigned},
		{"unassigned", NewUnassignedEvent(vm, "chat-1"), EventUnassigned},
		{"destroyed", NewDestroyedEvent(vm), EventDestroyed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.evt.Type() != tc.want {
				t.Errorf("Type() = %q, want %q", tc.evt.Type(), tc.want)
			}
			if tc.evt.VMID() != "vm-1" {
				t.Errorf("VMID() = %q, want vm-1", tc.evt.VMID())
			}
			if tc.evt.VM() == nil {
				t.Error("VM() returned nil")
			}
		})
	}

	// StatusChangedEvent carries the prior status.
	sce := NewStatusChangedEvent(vm, vmdomain.StatusIdle)
	if sce.Old() != vmdomain.StatusIdle {
		t.Errorf("Old() = %q, want %q", sce.Old(), vmdomain.StatusIdle)
	}
}

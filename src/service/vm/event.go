package vm

import (
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// EventType labels the kind of VM lifecycle transition an Event represents.
// Subscribers filter on this to react only to transitions they care about.
type EventType string

const (
	// EventCreated fires after a VM is first provisioned and persisted with
	// status Ready.
	EventCreated EventType = "vm.created"

	// EventStatusChanged fires after a VM's Status field transitions on a
	// persisted write (running → stopped via Stop or idle reap,
	// stopped → running via ResumeVM). Carries OldStatus.
	EventStatusChanged EventType = "vm.status_changed"

	// EventAssigned fires after a VM is bound to a chat via AssignToChat
	// or ResumeVM. Carries ChatID.
	EventAssigned EventType = "vm.assigned"

	// EventUnassigned fires after a VM's chat binding is released via
	// Unassign, Stop, or idle reap. ChatID carries the prior binding.
	EventUnassigned EventType = "vm.unassigned"

	// EventDestroyed fires after a VM's row is deleted from the repository.
	EventDestroyed EventType = "vm.destroyed"
)

// Event is the payload delivered to a LifecycleHook after a VM lifecycle
// transition. Subscribers can switch on Type() or type-assert to a concrete
// type for event-specific accessors (OldStatus, ChatID).
type Event interface {
	Type() EventType
	VMID() string
	VM() *vmdomain.VM
}

// CreatedEvent signals vm.created.
type CreatedEvent struct {
	vm *vmdomain.VM
}

func NewCreatedEvent(vm *vmdomain.VM) CreatedEvent { return CreatedEvent{vm: vm} }
func (e CreatedEvent) Type() EventType             { return EventCreated }
func (e CreatedEvent) VMID() string                { return e.vm.ID }
func (e CreatedEvent) VM() *vmdomain.VM            { return e.vm }

// StatusChangedEvent signals vm.status_changed. Old returns the status
// before the transition; the post-state VM() carries the new status.
type StatusChangedEvent struct {
	vm        *vmdomain.VM
	oldStatus vmdomain.Status
}

func NewStatusChangedEvent(vm *vmdomain.VM, oldStatus vmdomain.Status) StatusChangedEvent {
	return StatusChangedEvent{vm: vm, oldStatus: oldStatus}
}
func (e StatusChangedEvent) Type() EventType      { return EventStatusChanged }
func (e StatusChangedEvent) VMID() string         { return e.vm.ID }
func (e StatusChangedEvent) VM() *vmdomain.VM     { return e.vm }
func (e StatusChangedEvent) Old() vmdomain.Status { return e.oldStatus }

// AssignedEvent signals vm.assigned.
type AssignedEvent struct {
	vm     *vmdomain.VM
	chatID string
}

func NewAssignedEvent(vm *vmdomain.VM, chatID string) AssignedEvent {
	return AssignedEvent{vm: vm, chatID: chatID}
}
func (e AssignedEvent) Type() EventType  { return EventAssigned }
func (e AssignedEvent) VMID() string     { return e.vm.ID }
func (e AssignedEvent) VM() *vmdomain.VM { return e.vm }
func (e AssignedEvent) Chat() string     { return e.chatID }

// UnassignedEvent signals vm.unassigned. Chat returns the chat the VM was
// bound to before unassignment; empty if unknown.
type UnassignedEvent struct {
	vm     *vmdomain.VM
	chatID string
}

func NewUnassignedEvent(vm *vmdomain.VM, chatID string) UnassignedEvent {
	return UnassignedEvent{vm: vm, chatID: chatID}
}
func (e UnassignedEvent) Type() EventType  { return EventUnassigned }
func (e UnassignedEvent) VMID() string     { return e.vm.ID }
func (e UnassignedEvent) VM() *vmdomain.VM { return e.vm }
func (e UnassignedEvent) Chat() string     { return e.chatID }

// DestroyedEvent signals vm.destroyed.
type DestroyedEvent struct {
	vm *vmdomain.VM
}

func NewDestroyedEvent(vm *vmdomain.VM) DestroyedEvent { return DestroyedEvent{vm: vm} }
func (e DestroyedEvent) Type() EventType               { return EventDestroyed }
func (e DestroyedEvent) VMID() string                  { return e.vm.ID }
func (e DestroyedEvent) VM() *vmdomain.VM              { return e.vm }

// String is a convenience for logs and test diagnostics.
func (t EventType) String() string { return string(t) }

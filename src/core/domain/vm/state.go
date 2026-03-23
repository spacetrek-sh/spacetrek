// Package vm defines the VM state machine and state transitions.
package vm

import "fmt"

// StateTransition represents a valid VM state transition.
type StateTransition struct {
	From Status
	To   Status
}

// ValidTransitions defines all valid state transitions for a VM.
var ValidTransitions = []StateTransition{
	{StatusProvisioning, StatusReady},
	{StatusProvisioning, StatusTerminated}, // Failed provisioning

	{StatusReady, StatusRunning},
	{StatusReady, StatusIdle},
	{StatusReady, StatusTerminated},

	{StatusRunning, StatusIdle},
	{StatusRunning, StatusTerminated},

	{StatusIdle, StatusRunning},
	{StatusIdle, StatusTerminated},
}

// CanTransition checks if a state transition from 'from' to 'to' is valid.
func CanTransition(from, to Status) bool {
	for _, t := range ValidTransitions {
		if t.From == from && t.To == to {
			return true
		}
	}
	return false
}

// Transition performs a state transition on the VM entity.
// Returns an error if the transition is invalid.
func (v *VM) Transition(to Status) error {
	if !CanTransition(v.Status, to) {
		return &InvalidStateTransitionError{
			From: v.Status,
			To:   to,
		}
	}
	v.Status = to
	return nil
}

// IsTerminal checks if the status is a terminal state (no further transitions).
func (s Status) IsTerminal() bool {
	return s == StatusTerminated
}

// IsActive checks if the VM is in an active state (can execute tasks).
func (s Status) IsActive() bool {
	return s == StatusRunning || s == StatusReady
}

// IsIdle checks if the VM is idle and can be assigned to a session.
func (s Status) IsIdle() bool {
	return s == StatusIdle || s == StatusReady
}

// InvalidStateTransitionError represents an invalid state transition attempt.
type InvalidStateTransitionError struct {
	From Status
	To   Status
}

func (e *InvalidStateTransitionError) Error() string {
	return fmt.Sprintf("invalid state transition: %s → %s", e.From, e.To)
}

// StateMachine represents the VM state machine with transition hooks.
type StateMachine struct {
	vm *VM
}

// NewStateMachine creates a new state machine for a VM.
func NewStateMachine(vm *VM) *StateMachine {
	return &StateMachine{vm: vm}
}

// Current returns the current state of the VM.
func (sm *StateMachine) Current() Status {
	return sm.vm.Status
}

// TransitionTo attempts to transition to a new state.
func (sm *StateMachine) TransitionTo(to Status) error {
	if err := sm.vm.Transition(to); err != nil {
		return err
	}
	// Here you could add transition hooks (logging, metrics, etc.)
	return nil
}

// CanTransitionTo checks if a transition is possible without executing it.
func (sm *StateMachine) CanTransitionTo(to Status) bool {
	return CanTransition(sm.vm.Status, to)
}

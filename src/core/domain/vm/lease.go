package vm

import "time"

// Lease represents a VM assignment lease to a chat/session.
type Lease struct {
	ID         string     `json:"id"`
	ChatID     string     `json:"chat_id"`
	VMID       string     `json:"vm_id"`
	LeasedAt   time.Time  `json:"leased_at"`
	ReleasedAt *time.Time `json:"released_at,omitempty"`
}

package orchestrator

import "time"

// State is persisted per session to track orchestrator progress.
type State struct {
	SessionID string
	StepCount int
	UpdatedAt time.Time
}

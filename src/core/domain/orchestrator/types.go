package orchestrator

import "time"

// State is persisted per chat to track orchestrator progress.
type State struct {
	ChatID string
	StepCount int
	UpdatedAt time.Time
}

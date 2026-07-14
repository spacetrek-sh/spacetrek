package activator

import (
	"context"
	"errors"

	"golang.org/x/sync/singleflight"
)

// ErrActivationsFull is returned when the global activation semaphore is
// exhausted. Callers should respond with HTTP 503 + Retry-After.
var ErrActivationsFull = errors.New("too many concurrent activations")

// Activator deduplicates concurrent cold-start requests for the same VM
// (singleflight) and caps the total number of in-flight cold-starts
// globally (semaphore). A cold-start is the ResumeVM call; the actual
// HTTP forward happens after activation succeeds and is not gated.
type Activator struct {
	orch *OrchestratorClient

	sem chan struct{} // buffered cap=N; empty slot = available

	sf singleflight.Group
}

// NewActivator constructs an Activator with the given orchestrator client
// and concurrency cap.
func NewActivator(orch *OrchestratorClient, maxConcurrent int) *Activator {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Activator{
		orch: orch,
		sem:  make(chan struct{}, maxConcurrent),
	}
}

// Activate resumes the VM if needed. Multiple concurrent calls for the
// same vmID share one ResumeVM via singleflight. Returns the resumed VM
// info (or an error).
//
// The semaphore is held only across the ResumeVM call, not across the
// subsequent HTTP forward — that would gate warm traffic on cold-start
// capacity, which is wrong.
func (a *Activator) Activate(ctx context.Context, vmID string) (*VMInfo, error) {
	// singleflight collapses concurrent calls for the same vmID. The
	// semaphore is acquired INSIDE the singleflight leader — waiters
	// share the leader's slot. This means N concurrent requests for the
	// same idle VM count as 1 cold-start, not N.
	v, err, _ := a.sf.Do(vmID, func() (any, error) {
		select {
		case a.sem <- struct{}{}:
			defer func() { <-a.sem }()
		default:
			return nil, ErrActivationsFull
		}
		return a.orch.ResumeVM(ctx, vmID)
	})
	if err != nil {
		return nil, err
	}
	return v.(*VMInfo), nil
}

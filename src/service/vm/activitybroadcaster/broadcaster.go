// Package activitybroadcaster fans out recent runtime events to many SSE
// subscribers. One goroutine ticks at a fixed interval and runs a single
// repository query per tick (ListRecent, lookback-bounded); each subscriber
// receives the same slice and applies its own last-seen cursor in its own
// goroutine to dedupe events it has already emitted.
package activitybroadcaster

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	orchdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/orchestrator"
)

// Broadcaster owns the snapshot tick and the subscriber registry for runtime
// events.
type Broadcaster struct {
	repo     orchdomain.RuntimeEventRepository
	interval time.Duration
	lookback int

	mu          sync.RWMutex
	subscribers map[uint64]chan []*orchdomain.PersistedRuntimeEvent
	nextID      atomic.Uint64

	latestMu sync.RWMutex
	latest   []*orchdomain.PersistedRuntimeEvent
}

// New constructs a Broadcaster that refreshes from repo every interval,
// keeping the most recent `lookback` events available for immediate
// delivery to new subscribers.
func New(repo orchdomain.RuntimeEventRepository, interval time.Duration, lookback int) *Broadcaster {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if lookback <= 0 || lookback > 500 {
		lookback = 100
	}
	return &Broadcaster{
		repo:        repo,
		interval:    interval,
		lookback:    lookback,
		subscribers: make(map[uint64]chan []*orchdomain.PersistedRuntimeEvent),
	}
}

// Start runs the refresh loop until ctx is cancelled. Blocks; run in a
// goroutine. Errors are logged and the loop continues — a transient DB
// failure must not take the activity stream down.
func (b *Broadcaster) Start(ctx context.Context) {
	logger := pkglog.FromContext(ctx)
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()

	logger.InfoContext(ctx, "activity broadcaster started",
		"interval", b.interval.String(), "lookback", b.lookback)

	for {
		select {
		case <-ctx.Done():
			logger.InfoContext(ctx, "activity broadcaster stopped")
			b.closeAll()
			return
		case <-ticker.C:
			events, err := b.repo.ListRecent(ctx, b.lookback)
			if err != nil {
				logger.WarnContext(ctx, "activity broadcaster: list failed", "error", err)
				continue
			}
			b.storeLatest(events)
			b.broadcast(events)
		}
	}
}

// Subscribe returns a receive channel for event batches plus an unsubscribe
// func. If a batch is already available it is sent immediately so new SSE
// clients get the lookback window without waiting for the next tick. The
// channel is buffered (cap 1); slow consumers drop ticks silently — the
// next tick replaces the stale batch.
//
// Each batch contains the full lookback slice from the latest tick. The
// subscriber is responsible for filtering out events older than its own
// last-seen cursor (dedupe across ticks).
func (b *Broadcaster) Subscribe() (<-chan []*orchdomain.PersistedRuntimeEvent, func()) {
	id := b.nextID.Add(1)
	ch := make(chan []*orchdomain.PersistedRuntimeEvent, 1)

	b.mu.Lock()
	b.subscribers[id] = ch
	b.mu.Unlock()

	b.latestMu.RLock()
	latest := b.latest
	b.latestMu.RUnlock()
	if latest != nil {
		select {
		case ch <- latest:
		default:
		}
	}

	unsub := func() {
		b.mu.Lock()
		if c, ok := b.subscribers[id]; ok {
			delete(b.subscribers, id)
			close(c)
		}
		b.mu.Unlock()
	}
	return ch, unsub
}

func (b *Broadcaster) broadcast(events []*orchdomain.PersistedRuntimeEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subscribers {
		select {
		case ch <- events:
		default:
			// Subscriber is behind; drop this tick. SSE is best-effort.
		}
	}
}

func (b *Broadcaster) storeLatest(events []*orchdomain.PersistedRuntimeEvent) {
	b.latestMu.Lock()
	b.latest = events
	b.latestMu.Unlock()
}

func (b *Broadcaster) closeAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, ch := range b.subscribers {
		close(ch)
		delete(b.subscribers, id)
	}
}

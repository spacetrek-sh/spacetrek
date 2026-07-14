// Package fleetbroadcaster fans out a single VM snapshot to many SSE
// subscribers. One goroutine ticks at a fixed interval and runs a single
// repository query per tick; each subscriber receives the same snapshot
// and applies its own filter/pagination in its own goroutine.
package fleetbroadcaster

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	vmdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/vm"
)

// Broadcaster owns the snapshot tick and the subscriber registry.
type Broadcaster struct {
	repo     vmdomain.Repository
	interval time.Duration

	mu          sync.RWMutex
	subscribers map[uint64]chan []*vmdomain.VM
	nextID      atomic.Uint64

	latestMu sync.RWMutex
	latest   []*vmdomain.VM
}

// New constructs a Broadcaster that refreshes from repo every interval.
func New(repo vmdomain.Repository, interval time.Duration) *Broadcaster {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &Broadcaster{
		repo:        repo,
		interval:    interval,
		subscribers: make(map[uint64]chan []*vmdomain.VM),
	}
}

// Start runs the refresh loop until ctx is cancelled. Blocks; run in a
// goroutine. Errors are logged and the loop continues — a transient DB
// failure must not take the fleet stream down.
func (b *Broadcaster) Start(ctx context.Context) {
	logger := pkglog.FromContext(ctx)
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()

	logger.InfoContext(ctx, "fleet broadcaster started", "interval", b.interval.String())

	for {
		select {
		case <-ctx.Done():
			logger.InfoContext(ctx, "fleet broadcaster stopped")
			b.closeAll()
			return
		case <-ticker.C:
			vms, err := b.repo.List(ctx)
			if err != nil {
				logger.WarnContext(ctx, "fleet broadcaster: list failed", "error", err)
				continue
			}
			b.storeLatest(vms)
			b.broadcast(vms)
		}
	}
}

// Subscribe returns a receive channel for snapshots plus an unsubscribe
// func. If a snapshot is already available it is sent immediately so new
// SSE clients don't wait for the next tick. The channel is buffered (cap 1);
// slow consumers drop ticks silently — the next tick replaces the stale one.
func (b *Broadcaster) Subscribe() (<-chan []*vmdomain.VM, func()) {
	id := b.nextID.Add(1)
	ch := make(chan []*vmdomain.VM, 1)

	b.mu.Lock()
	b.subscribers[id] = ch
	b.mu.Unlock()

	// Send the latest snapshot immediately if we have one.
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

func (b *Broadcaster) broadcast(vms []*vmdomain.VM) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subscribers {
		select {
		case ch <- vms:
		default:
			// Subscriber is behind; drop this tick. SSE is best-effort.
		}
	}
}

func (b *Broadcaster) storeLatest(vms []*vmdomain.VM) {
	b.latestMu.Lock()
	b.latest = vms
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

package memory

import (
	"context"
	"sync"
	"time"

	orchdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/orchestrator"
)

// RuntimeEventRepository is a thread-safe, in-memory implementation of
// orchdomain.RuntimeEventRepository.
type RuntimeEventRepository struct {
	mu     sync.RWMutex
	events map[string][]*orchdomain.PersistedRuntimeEvent
}

func NewRuntimeEventRepository() *RuntimeEventRepository {
	return &RuntimeEventRepository{events: make(map[string][]*orchdomain.PersistedRuntimeEvent)}
}

func (r *RuntimeEventRepository) Insert(_ context.Context, event *orchdomain.PersistedRuntimeEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events[event.ChatID] = append(r.events[event.ChatID], event)
	return nil
}

func (r *RuntimeEventRepository) ListByChatID(_ context.Context, params orchdomain.ListRuntimeEventsParams) (*orchdomain.ListRuntimeEventsResult, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if params.Limit <= 0 || params.Limit > 200 {
		params.Limit = 100
	}

	events := r.events[params.ChatID]

	var start int
	if params.Cursor != nil {
		for i, e := range events {
			if e.CreatedAt.After(*params.Cursor) {
				start = i
				break
			}
			start = i + 1
		}
	}

	end := start + params.Limit + 1
	if end > len(events) {
		end = len(events)
	}
	page := events[start:end]

	hasMore := len(page) > params.Limit
	if hasMore {
		page = page[:params.Limit]
	}

	items := make([]*orchdomain.PersistedRuntimeEvent, len(page))
	for i, e := range page {
		cp := *e
		items[i] = &cp
	}

	var nextCursor *time.Time
	if hasMore && len(items) > 0 {
		t := items[len(items)-1].CreatedAt
		nextCursor = &t
	}

	return &orchdomain.ListRuntimeEventsResult{
		Items:      items,
		NextCursor: nextCursor,
	}, nil
}

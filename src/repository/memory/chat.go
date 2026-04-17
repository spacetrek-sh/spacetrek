package memory

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/kumori-sh/spacetrk/pkg/exception"
	"github.com/kumori-sh/spacetrk/src/core/domain/chat"
)

// ChatRepository is a thread-safe, in-memory implementation of chat.Repository.
type ChatRepository struct {
	mu    sync.RWMutex
	chats map[string]*chat.Chat
}

func NewChatRepository() *ChatRepository {
	return &ChatRepository{chats: make(map[string]*chat.Chat)}
}

func (r *ChatRepository) Create(_ context.Context, c *chat.Chat) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *c
	cp.Messages = cloneMessages(c.Messages)
	r.chats[c.ID] = &cp
	return nil
}

func (r *ChatRepository) GetByID(_ context.Context, id string) (*chat.Chat, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.chats[id]
	if !ok {
		return nil, exception.NotFound("chat", id)
	}
	cp := *c
	cp.Messages = cloneMessages(c.Messages)
	return &cp, nil
}

func (r *ChatRepository) Update(_ context.Context, c *chat.Chat) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.chats[c.ID]; !ok {
		return exception.NotFound("chat", c.ID)
	}
	cp := *c
	cp.Messages = cloneMessages(c.Messages)
	r.chats[c.ID] = &cp
	return nil
}

func (r *ChatRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.chats[id]; !ok {
		return exception.NotFound("chat", id)
	}
	delete(r.chats, id)
	return nil
}

func (r *ChatRepository) ListByUserID(_ context.Context, params chat.ListParams) (*chat.ListResult, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if params.Limit <= 0 || params.Limit > 100 {
		params.Limit = 20
	}

	// Collect and sort chats for this user by CreatedAt DESC, ID DESC.
	var sorted []*chat.Chat
	for _, c := range r.chats {
		if c.UserID == params.UserID {
			sorted = append(sorted, c)
		}
	}
	slices.SortFunc(sorted, func(a, b *chat.Chat) int {
		if a.CreatedAt.After(b.CreatedAt) {
			return -1
		}
		if a.CreatedAt.Before(b.CreatedAt) {
			return 1
		}
		if a.ID > b.ID {
			return -1
		}
		if a.ID < b.ID {
			return 1
		}
		return 0
	})

	// Seek past cursor.
	start := 0
	if params.Cursor != nil {
		for i, c := range sorted {
			if c.CreatedAt.Before(params.Cursor.CreatedAt) ||
				(c.CreatedAt.Equal(params.Cursor.CreatedAt) && c.ID < params.Cursor.ID) {
				start = i
				break
			}
			start = i + 1
		}
	}

	// Take limit + 1 to detect has-more.
	end := start + params.Limit + 1
	if end > len(sorted) {
		end = len(sorted)
	}
	page := sorted[start:end]

	hasMore := len(page) > params.Limit
	if hasMore {
		page = page[:params.Limit]
	}

	items := make([]*chat.ConversationSummary, len(page))
	for i, c := range page {
		s := &chat.ConversationSummary{
			ID:        c.ID,
			AgentID:   c.AgentID,
			UserID:    c.UserID,
			Title:     c.Title,
			VMID:      c.VMID,
			Status:    c.Status,
			CreatedAt: c.CreatedAt,
			UpdatedAt: c.UpdatedAt,
		}
		if len(c.Messages) > 0 {
			last := c.Messages[len(c.Messages)-1]
			s.LastMessage = last.Content
			s.LastMessageAt = last.At
		}
		items[i] = s
	}

	var nextCursor *chat.ListCursor
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		nextCursor = &chat.ListCursor{
			CreatedAt: last.CreatedAt,
			ID:        last.ID,
		}
	}

	return &chat.ListResult{
		Items:      items,
		NextCursor: nextCursor,
	}, nil
}

func (r *ChatRepository) ListMessages(_ context.Context, params chat.ListMessagesParams) (*chat.ListMessagesResult, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if params.Limit <= 0 || params.Limit > 100 {
		params.Limit = 50
	}

	c, ok := r.chats[params.ChatID]
	if !ok {
		return nil, exception.NotFound("chat", params.ChatID)
	}

	// Messages are stored in append order (ASC by sequence). Reverse for DESC.
	total := len(c.Messages)
	start := total // past the end = empty
	if params.Cursor != nil {
		// Seek: skip messages with sequence >= cursor (already sent).
		for i := total - 1; i >= 0; i-- {
			if int64(i+1) < params.Cursor.SequenceNumber {
				start = i
				break
			}
			start = i
		}
		if params.Cursor != nil && start == 0 && int64(1) >= params.Cursor.SequenceNumber {
			// All messages have sequence >= cursor, nothing left.
			return &chat.ListMessagesResult{Items: []*chat.MessageSummary{}}, nil
		}
	} else {
		start = total - 1
	}

	if start < 0 {
		start = -1
	}

	// Collect from start downwards (DESC order).
	count := 0
	var items []*chat.MessageSummary
	for i := start; i >= 0 && count < params.Limit+1; i-- {
		msg := c.Messages[i]
		items = append(items, &chat.MessageSummary{
			ID:             fmt.Sprintf("%s-%d", params.ChatID, i+1),
			SequenceNumber: int64(i + 1),
			Role:           msg.Role,
			Content:        msg.Content,
			Metadata:       msg.Metadata,
			At:             msg.At,
		})
		count++
	}

	hasMore := len(items) > params.Limit
	if hasMore {
		items = items[:params.Limit]
	}

	var nextCursor *chat.MessageCursor
	if hasMore && len(items) > 0 {
		nextCursor = &chat.MessageCursor{
			SequenceNumber: items[len(items)-1].SequenceNumber,
		}
	}

	return &chat.ListMessagesResult{
		Items:      items,
		NextCursor: nextCursor,
	}, nil
}

func cloneMessages(messages []chat.Message) []chat.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]chat.Message, len(messages))
	for i, msg := range messages {
		cloned[i] = msg
		if len(msg.Metadata) > 0 {
			cloned[i].Metadata = make(map[string]any, len(msg.Metadata))
			for k, v := range msg.Metadata {
				cloned[i].Metadata[k] = v
			}
		}
	}
	return cloned
}

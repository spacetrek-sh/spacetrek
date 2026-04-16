package memory

import (
	"context"
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

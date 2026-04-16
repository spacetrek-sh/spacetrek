package chatsvc

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/kumori-sh/spacetrk/pkg/exception"
	pkglog "github.com/kumori-sh/spacetrk/pkg/log"
	"github.com/kumori-sh/spacetrk/src/core/domain/agent"
	"github.com/kumori-sh/spacetrk/src/core/domain/chat"
	orchdomain "github.com/kumori-sh/spacetrk/src/core/domain/orchestrator"
	orchestratorsvc "github.com/kumori-sh/spacetrk/src/service/orchestrator"
)

// Orchestrator defines the runtime behavior expected by chat message flow.
type Orchestrator interface {
	Process(ctx context.Context, input orchestratorsvc.ProcessInput) (orchestratorsvc.ProcessResult, error)
}

// Service implements the chat business logic.
type Service struct {
	chats  chat.Repository
	agents agent.Repository
	orch   Orchestrator

	mu          sync.RWMutex
	subscribers map[string]map[uint64]chan orchdomain.RuntimeEvent
	subscriberN atomic.Uint64
}

func New(chats chat.Repository, agents agent.Repository, orch Orchestrator) *Service {
	return &Service{
		chats:       chats,
		agents:      agents,
		orch:        orch,
		subscribers: make(map[string]map[uint64]chan orchdomain.RuntimeEvent),
	}
}

// Create opens a new chat, verifying that the requested agent exists first.
func (s *Service) Create(ctx context.Context, p chat.CreateParams) (*chat.Chat, error) {
	logger := pkglog.FromContext(ctx)

	logger.DebugContext(ctx, "chat create: starting", "agent_id", p.AgentID, "user_id", p.UserID)

	p.AgentID = strings.TrimSpace(p.AgentID)
	p.UserID = strings.TrimSpace(p.UserID)
	p.AgentName = strings.TrimSpace(p.AgentName)
	p.Model = strings.TrimSpace(p.Model)

	if p.AgentID == "" {
		logger.DebugContext(ctx, "chat create: no agent_id provided, creating default agent", "agent_name", p.AgentName, "model", p.Model)
		created, err := s.createDefaultAgent(ctx, p)
		if err != nil {
			return nil, err
		}
		p.AgentID = created.ID
		logger.DebugContext(ctx, "chat create: default agent created", "agent_id", created.ID)
	} else {
		if _, err := s.agents.GetByID(ctx, p.AgentID); err != nil {
			logger.WarnContext(ctx, "agent not found for chat", "agent_id", p.AgentID, "error", err)
			return nil, err
		}
	}

	if p.UserID == "" {
		p.UserID = "anonymous-" + uuid.NewString()
	}

	c := chat.New(p)
	if err := s.chats.Create(ctx, c); err != nil {
		logger.ErrorContext(ctx, "failed to persist chat", "error", err)
		return nil, err
	}

	logger.DebugContext(ctx, "chat create: persisted", "chat_id", c.ID)
	logger.InfoContext(ctx, "chat created", "chat_id", c.ID, "agent_id", c.AgentID, "user_id", c.UserID)
	return c, nil
}

func (s *Service) createDefaultAgent(ctx context.Context, p chat.CreateParams) (*agent.Agent, error) {
	name := p.AgentName
	if name == "" {
		name = "Chat Assistant"
	}

	model := p.Model
	if model == "" {
		model = "gemini"
	}

	created := agent.New(agent.CreateParams{
		UserID:       p.UserID,
		Name:         name,
		Description:  "Auto-generated assistant for chat",
		Model:        model,
		SystemPrompt: p.SystemPrompt,
	})

	if err := s.agents.Create(ctx, created); err != nil {
		return nil, err
	}

	return created, nil
}

func (s *Service) Get(ctx context.Context, id string) (*chat.Chat, error) {
	logger := pkglog.FromContext(ctx)
	logger.DebugContext(ctx, "chat get: fetching", "chat_id", id)
	return s.chats.GetByID(ctx, id)
}

// SendOrCreate sends a message to an existing chat or auto-creates one first.
func (s *Service) SendOrCreate(ctx context.Context, id, content string, p chat.CreateParams) (*chat.Chat, error) {
	logger := pkglog.FromContext(ctx)

	if id != "" {
		logger.DebugContext(ctx, "chat send or create: continuing existing", "chat_id", id)
		return s.SendMessage(ctx, id, content, "")
	}

	logger.DebugContext(ctx, "chat send or create: auto-creating new chat")
	c, err := s.Create(ctx, p)
	if err != nil {
		return nil, err
	}
	return s.SendMessage(ctx, c.ID, content, "")
}

// SendMessage appends user input, runs orchestrator flow, and persists updates.
func (s *Service) SendMessage(ctx context.Context, id, content, vmID string) (*chat.Chat, error) {
	logger := pkglog.FromContext(ctx)

	logger.DebugContext(ctx, "chat send message: starting", "chat_id", id, "message_len", len(content), "vm_id", vmID)

	c, err := s.chats.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if c.Status == chat.StatusClosed {
		return nil, exception.BadRequest("chat is already closed")
	}

	c.AddMessage(chat.RoleUser, content)
	logger.DebugContext(ctx, "chat send message: user message appended", "chat_id", id, "history_count", len(c.Messages))

	if s.orch != nil {
		logger.DebugContext(ctx, "chat send message: calling orchestrator", "chat_id", id)

		emit := func(event orchdomain.RuntimeEvent) {
			if event.ChatID == "" {
				event.ChatID = id
			}
			if event.At.IsZero() {
				event.At = time.Now().UTC()
			}
			s.publish(event)
		}

		result, err := s.orch.Process(ctx, orchestratorsvc.ProcessInput{
			ChatID:    id,
			AgentID:   c.AgentID,
			UserID:    c.UserID,
			Message:   content,
			VMID:      vmID,
			History:   c.Messages,
			EmitEvent: emit,
		})
		if err != nil {
			logger.ErrorContext(ctx, "orchestrator process failed", "chat_id", id, "error", err)
			s.publish(orchdomain.RuntimeEvent{
				Type:   orchdomain.EventAgentError,
				ChatID: id,
				Error:  err.Error(),
				At:     time.Now().UTC(),
			})
			return nil, err
		}

		assistant := result.AssistantMessage
		if assistant == "" {
			assistant = "[orchestrator returned empty response]"
		}
		c.AddMessageWithMetadata(chat.RoleAssistant, assistant, buildAssistantMetadata(result.Trace))
		logger.DebugContext(ctx, "chat send message: orchestrator completed", "chat_id", id, "tool_results", len(result.ToolResults), "response_len", len(assistant))
	} else {
		c.AddMessage(chat.RoleAssistant, "[orchestrator not configured]")
		logger.DebugContext(ctx, "chat send message: no orchestrator configured", "chat_id", id)
	}

	if err := s.chats.Update(ctx, c); err != nil {
		logger.ErrorContext(ctx, "failed to persist chat after message", "chat_id", id, "error", err)
		return nil, err
	}

	logger.DebugContext(ctx, "chat send message: chat persisted", "chat_id", id, "total_messages", len(c.Messages))
	logger.InfoContext(ctx, "chat message processed", "chat_id", id)
	return c, nil
}

// SubscribeRuntimeEvents registers a stream consumer for one chat.
func (s *Service) SubscribeRuntimeEvents(ctx context.Context, chatID string) (<-chan orchdomain.RuntimeEvent, error) {
	logger := pkglog.FromContext(ctx)

	if _, err := s.chats.GetByID(ctx, chatID); err != nil {
		return nil, err
	}

	ch := make(chan orchdomain.RuntimeEvent, 32)
	id := s.subscriberN.Add(1)

	s.mu.Lock()
	if s.subscribers[chatID] == nil {
		s.subscribers[chatID] = make(map[uint64]chan orchdomain.RuntimeEvent)
	}
	s.subscribers[chatID][id] = ch
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.unsubscribe(chatID, id)
	}()

	logger.DebugContext(ctx, "runtime events subscriber registered", "chat_id", chatID)
	return ch, nil
}

func (s *Service) publish(event orchdomain.RuntimeEvent) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	listeners := s.subscribers[event.ChatID]
	for _, ch := range listeners {
		select {
		case ch <- event:
		default:
		}
	}
}

func (s *Service) unsubscribe(chatID string, id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	listeners := s.subscribers[chatID]
	if listeners == nil {
		return
	}

	if ch, ok := listeners[id]; ok {
		delete(listeners, id)
		close(ch)
	}

	if len(listeners) == 0 {
		delete(s.subscribers, chatID)
	}
}

func buildAssistantMetadata(trace *orchdomain.ExecutionTrace) map[string]any {
	if trace == nil {
		return nil
	}

	execution := map[string]any{
		"trace_id":       trace.TraceID,
		"execution_mode": trace.ExecutionMode,
		"reasoning":      trace.Reasoning,
		"steps":          trace.Steps,
		"final_answer":   trace.FinalAnswer,
		"started_at":     trace.StartedAt,
		"completed_at":   trace.CompletedAt,
	}
	if !trace.TokenUsage.IsZero() {
		execution["token_usage"] = trace.TokenUsage
	}

	return map[string]any{
		"execution": execution,
	}
}

// Close marks the chat as closed.
func (s *Service) Close(ctx context.Context, id string) error {
	logger := pkglog.FromContext(ctx)

	logger.DebugContext(ctx, "chat close: starting", "chat_id", id)

	c, err := s.chats.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if c.Status == chat.StatusClosed {
		logger.DebugContext(ctx, "chat close: already closed, skipping", "chat_id", id)
		return nil // idempotent
	}
	c.Status = chat.StatusClosed
	c.UpdatedAt = time.Now().UTC()
	if err := s.chats.Update(ctx, c); err != nil {
		logger.ErrorContext(ctx, "failed to close chat", "chat_id", id, "error", err)
		return err
	}

	logger.DebugContext(ctx, "chat close: persisted", "chat_id", id)
	logger.InfoContext(ctx, "chat closed", "chat_id", id)
	return nil
}

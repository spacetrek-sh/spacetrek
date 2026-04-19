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
	chats      chat.Repository
	runtimes   orchdomain.RuntimeEventRepository
	agents     agent.Repository
	orch       Orchestrator
	vmResolver VMResolver

	mu          sync.RWMutex
	subscribers map[string]map[uint64]chan orchdomain.RuntimeEvent
	subscriberN atomic.Uint64

	eventBufMu sync.Mutex
	eventBufs  map[string][]orchdomain.RuntimeEvent
}

func New(chats chat.Repository, runtimes orchdomain.RuntimeEventRepository, agents agent.Repository, orch Orchestrator, vmRes VMResolver) *Service {
	return &Service{
		chats:       chats,
		runtimes:    runtimes,
		agents:      agents,
		orch:        orch,
		vmResolver:  vmRes,
		subscribers: make(map[string]map[uint64]chan orchdomain.RuntimeEvent),
		eventBufs:   make(map[string][]orchdomain.RuntimeEvent),
	}
}

// resolveVMID attempts to find or resume a VM for the given chat.
// Returns empty string (no error) if no VM is available or resolver is nil.
func (s *Service) resolveVMID(ctx context.Context, chatID string) string {
	if s.vmResolver == nil {
		return ""
	}
	vmID, err := s.vmResolver.ResolveVMForChat(ctx, chatID)
	if err != nil {
		logger := pkglog.FromContext(ctx)
		logger.WarnContext(ctx, "failed to resolve VM for chat, proceeding without VM", "chat_id", chatID, "error", err)
		return ""
	}
	return vmID
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

// SendOrCreateAsync is the async variant — returns the chat immediately,
// runs orchestration in the background, streams events to SSE subscribers.
func (s *Service) SendOrCreateAsync(ctx context.Context, id, content string, p chat.CreateParams) (string, error) {
	if id == "" {
		c, err := s.Create(ctx, p)
		if err != nil {
			return "", err
		}
		id = c.ID
	}

	if err := s.SendMessageAsync(ctx, id, content, ""); err != nil {
		return "", err
	}
	return id, nil
}

// SendMessageAsync validates, persists the user message, launches orchestration
// in a background goroutine, and returns immediately.
// Events are published to SSE subscribers. A processing_done event signals completion.
func (s *Service) SendMessageAsync(ctx context.Context, id, content, vmID string) error {
	logger := pkglog.FromContext(ctx)

	logger.DebugContext(ctx, "chat send message async: starting", "chat_id", id, "message_len", len(content), "vm_id", vmID)

	c, err := s.chats.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if c.Status == chat.StatusClosed {
		return exception.BadRequest("chat is already closed")
	}

	c.AddMessage(chat.RoleUser, content)
	if err := s.chats.Update(ctx, c); err != nil {
		logger.ErrorContext(ctx, "failed to persist user message", "chat_id", id, "error", err)
		return err
	}
	logger.DebugContext(ctx, "chat send message async: user message persisted", "chat_id", id, "history_count", len(c.Messages))

	chatID := id
	agentID := c.AgentID
	userID := c.UserID

	go func() {
		bgCtx := context.Background()
		bgLogger := pkglog.FromContext(bgCtx).With("chat_id", chatID)
		bgCtx = pkglog.WithLogger(bgCtx, bgLogger)

		if s.orch == nil {
			bgLogger.WarnContext(bgCtx, "no orchestrator configured")
			errEvent := orchdomain.RuntimeEvent{
				Type:   orchdomain.EventError,
				ChatID: chatID,
				Error:  "orchestrator not configured",
				At:     time.Now().UTC(),
			}
			s.publish(errEvent)
			s.persistEvent(chatID, errEvent)
			return
		}

		emit := func(event orchdomain.RuntimeEvent) {
			if event.ChatID == "" {
				event.ChatID = chatID
			}
			if event.At.IsZero() {
				event.At = time.Now().UTC()
			}
			s.publish(event)
			s.persistEvent(chatID, event)
		}

		bgLogger.DebugContext(bgCtx, "async orchestrator: starting")

		resolvedVMID := vmID
		if resolvedVMID == "" {
			resolvedVMID = s.resolveVMID(bgCtx, chatID)
		}
		if resolvedVMID != "" {
			bgLogger.DebugContext(bgCtx, "async orchestrator: resolved VM", "vm_id", resolvedVMID)
		}

		result, err := s.orch.Process(bgCtx, orchestratorsvc.ProcessInput{
			ChatID:    chatID,
			AgentID:   agentID,
			UserID:    userID,
			Message:   content,
			VMID:      resolvedVMID,
			History:   c.Messages,
			EmitEvent: emit,
		})

		if err != nil {
			bgLogger.ErrorContext(bgCtx, "async orchestrator failed", "error", err)
			errEvent := orchdomain.RuntimeEvent{
				Type:   orchdomain.EventError,
				ChatID: chatID,
				Error:  err.Error(),
				At:     time.Now().UTC(),
			}
			s.publish(errEvent)
			s.persistEvent(chatID, errEvent)
			return
		}

		assistant := result.AssistantMessage
		if assistant == "" {
			assistant = "[orchestrator returned empty response]"
		}

		updated, err := s.chats.GetByID(bgCtx, chatID)
		if err != nil {
			bgLogger.ErrorContext(bgCtx, "failed to reload chat for persisting assistant", "error", err)
			return
		}
		updated.AddMessageWithMetadata(chat.RoleAssistant, assistant, buildAssistantMetadata(result.Trace))
		if err := s.chats.Update(bgCtx, updated); err != nil {
			bgLogger.ErrorContext(bgCtx, "failed to persist assistant message", "error", err)
			return
		}

		bgLogger.DebugContext(bgCtx, "async orchestrator completed", "response_len", len(assistant))

		doneEvent := orchdomain.RuntimeEvent{
			Type:       orchdomain.EventDone,
			ChatID:     chatID,
			Data:       assistant,
			TokenUsage: tokenUsagePtr(result.Trace),
			At:         time.Now().UTC(),
		}
		s.publish(doneEvent)
		s.persistEvent(chatID, doneEvent)
	}()

	return nil
}

func tokenUsagePtr(trace *orchdomain.ExecutionTrace) *orchdomain.TokenUsage {
	if trace == nil || trace.TokenUsage.IsZero() {
		return nil
	}
	copy := trace.TokenUsage
	return &copy
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
			s.persistEvent(id, event)
		}

		resolvedVMID := vmID
		if resolvedVMID == "" {
			resolvedVMID = s.resolveVMID(ctx, id)
		}
		if resolvedVMID != "" {
			logger.DebugContext(ctx, "chat send message: resolved VM", "vm_id", resolvedVMID)
		}

		result, err := s.orch.Process(ctx, orchestratorsvc.ProcessInput{
			ChatID:    id,
			AgentID:   c.AgentID,
			UserID:    c.UserID,
			Message:   content,
			VMID:      resolvedVMID,
			History:   c.Messages,
			EmitEvent: emit,
		})
		if err != nil {
			logger.ErrorContext(ctx, "orchestrator process failed", "chat_id", id, "error", err)
			errEvent := orchdomain.RuntimeEvent{
				Type:   orchdomain.EventError,
				ChatID: id,
				Error:  err.Error(),
				At:     time.Now().UTC(),
			}
			s.publish(errEvent)
			s.persistEvent(id, errEvent)
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

	// Replay buffered events so late subscribers don't miss anything.
	s.eventBufMu.Lock()
	buf := s.eventBufs[chatID]
	s.eventBufMu.Unlock()

	if len(buf) > 0 {
		go func() {
			for _, evt := range buf {
				select {
				case ch <- evt:
				default:
				}
			}
		}()
	}

	return ch, nil
}

func (s *Service) publish(event orchdomain.RuntimeEvent) {
	// Buffer event for late subscribers.
	s.eventBufMu.Lock()
	s.eventBufs[event.ChatID] = append(s.eventBufs[event.ChatID], event)
	// Keep buffer bounded: clear on done.
	if event.Type == orchdomain.EventDone {
		delete(s.eventBufs, event.ChatID)
	}
	s.eventBufMu.Unlock()

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

// persistEvent saves a runtime event via direct INSERT into runtime_events table.
func (s *Service) persistEvent(chatID string, event orchdomain.RuntimeEvent) {
	ctx := context.Background()
	logger := pkglog.FromContext(ctx)

	persisted := event.ToPersisted()
	if persisted.ChatID == "" {
		persisted.ChatID = chatID
	}

	if err := s.runtimes.Insert(ctx, persisted); err != nil {
		logger.WarnContext(ctx, "failed to persist runtime event",
			"chat_id", chatID, "event_type", event.Type, "error", err)
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

func (s *Service) ListConversations(ctx context.Context, params chat.ListParams) (*chat.ListResult, error) {
	logger := pkglog.FromContext(ctx)
	if params.Limit <= 0 || params.Limit > 100 {
		params.Limit = 20
	}
	result, err := s.chats.ListByUserID(ctx, params)
	if err != nil {
		logger.ErrorContext(ctx, "list conversations failed", "user_id", params.UserID, "error", err)
		return nil, err
	}
	return result, nil
}

func (s *Service) ListMessages(ctx context.Context, params chat.ListMessagesParams) (*chat.ListMessagesResult, error) {
	logger := pkglog.FromContext(ctx)
	if params.Limit <= 0 || params.Limit > 100 {
		params.Limit = 50
	}
	result, err := s.chats.ListMessages(ctx, params)
	if err != nil {
		logger.ErrorContext(ctx, "list messages failed", "chat_id", params.ChatID, "error", err)
		return nil, err
	}
	return result, nil
}

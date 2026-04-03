package sessionsvc

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
	orchdomain "github.com/kumori-sh/spacetrk/src/core/domain/orchestrator"
	"github.com/kumori-sh/spacetrk/src/core/domain/session"
	orchestratorsvc "github.com/kumori-sh/spacetrk/src/service/orchestrator"
)

// Orchestrator defines the runtime behavior expected by session message flow.
type Orchestrator interface {
	Process(ctx context.Context, input orchestratorsvc.ProcessInput) (orchestratorsvc.ProcessResult, error)
}

// Service implements the session business logic.
type Service struct {
	sessions session.Repository
	agents   agent.Repository
	orch     Orchestrator

	mu          sync.RWMutex
	subscribers map[string]map[uint64]chan orchdomain.RuntimeEvent
	subscriberN atomic.Uint64
}

func New(sessions session.Repository, agents agent.Repository, orch Orchestrator) *Service {
	return &Service{
		sessions:    sessions,
		agents:      agents,
		orch:        orch,
		subscribers: make(map[string]map[uint64]chan orchdomain.RuntimeEvent),
	}
}

// Create opens a new session, verifying that the requested agent exists first.
func (s *Service) Create(ctx context.Context, p session.CreateParams) (*session.Session, error) {
	logger := pkglog.FromContext(ctx)

	p.AgentID = strings.TrimSpace(p.AgentID)
	p.UserID = strings.TrimSpace(p.UserID)
	p.AgentName = strings.TrimSpace(p.AgentName)
	p.Model = strings.TrimSpace(p.Model)

	if p.AgentID == "" {
		created, err := s.createDefaultAgent(ctx, p)
		if err != nil {
			return nil, err
		}
		p.AgentID = created.ID
	} else {
		if _, err := s.agents.GetByID(ctx, p.AgentID); err != nil {
			logger.WarnContext(ctx, "agent not found for session", "agent_id", p.AgentID, "error", err)
			return nil, err
		}
	}

	if p.UserID == "" {
		p.UserID = "anonymous-" + uuid.NewString()
	}

	sess := session.New(p)
	if err := s.sessions.Create(ctx, sess); err != nil {
		logger.ErrorContext(ctx, "failed to persist session", "error", err)
		return nil, err
	}

	logger.InfoContext(ctx, "session created", "session_id", sess.ID, "agent_id", sess.AgentID, "user_id", sess.UserID)
	return sess, nil
}

func (s *Service) createDefaultAgent(ctx context.Context, p session.CreateParams) (*agent.Agent, error) {
	name := p.AgentName
	if name == "" {
		name = "Chat Assistant"
	}

	model := p.Model
	if model == "" {
		model = "gemini"
	}

	created := agent.New(agent.CreateParams{
		Name:         name,
		Description:  "Auto-generated assistant for session",
		Model:        model,
		SystemPrompt: p.SystemPrompt,
	})

	if err := s.agents.Create(ctx, created); err != nil {
		return nil, err
	}

	return created, nil
}

func (s *Service) Get(ctx context.Context, id string) (*session.Session, error) {
	return s.sessions.GetByID(ctx, id)
}

// SendMessage appends user input, runs orchestrator flow, and persists updates.
func (s *Service) SendMessage(ctx context.Context, id, content, vmID string) (*session.Session, error) {
	logger := pkglog.FromContext(ctx)

	sess, err := s.sessions.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if sess.Status == session.StatusClosed {
		return nil, exception.BadRequest("session is already closed")
	}

	sess.AddMessage(session.RoleUser, content)

	if s.orch != nil {
		emit := func(event orchdomain.RuntimeEvent) {
			if event.SessionID == "" {
				event.SessionID = id
			}
			if event.At.IsZero() {
				event.At = time.Now().UTC()
			}
			s.publish(event)
		}

		result, err := s.orch.Process(ctx, orchestratorsvc.ProcessInput{
			SessionID: id,
			AgentID:   sess.AgentID,
			UserID:    sess.UserID,
			Message:   content,
			VMID:      vmID,
			History:   sess.Messages,
			EmitEvent: emit,
		})
		if err != nil {
			logger.ErrorContext(ctx, "orchestrator process failed", "session_id", id, "error", err)
			s.publish(orchdomain.RuntimeEvent{
				Type:      orchdomain.EventAgentError,
				SessionID: id,
				Error:     err.Error(),
				At:        time.Now().UTC(),
			})
			return nil, err
		}

		assistant := result.AssistantMessage
		if assistant == "" {
			assistant = "[orchestrator returned empty response]"
		}
		sess.AddMessage(session.RoleAssistant, assistant)
	} else {
		sess.AddMessage(session.RoleAssistant, "[orchestrator not configured]")
	}

	if err := s.sessions.Update(ctx, sess); err != nil {
		logger.ErrorContext(ctx, "failed to persist session after message", "session_id", id, "error", err)
		return nil, err
	}

	logger.InfoContext(ctx, "session message processed", "session_id", id)
	return sess, nil
}

// SubscribeRuntimeEvents registers a stream consumer for one session.
func (s *Service) SubscribeRuntimeEvents(ctx context.Context, sessionID string) (<-chan orchdomain.RuntimeEvent, error) {
	logger := pkglog.FromContext(ctx)

	if _, err := s.sessions.GetByID(ctx, sessionID); err != nil {
		return nil, err
	}

	ch := make(chan orchdomain.RuntimeEvent, 32)
	id := s.subscriberN.Add(1)

	s.mu.Lock()
	if s.subscribers[sessionID] == nil {
		s.subscribers[sessionID] = make(map[uint64]chan orchdomain.RuntimeEvent)
	}
	s.subscribers[sessionID][id] = ch
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.unsubscribe(sessionID, id)
	}()

	logger.DebugContext(ctx, "runtime events subscriber registered", "session_id", sessionID)
	return ch, nil
}

func (s *Service) publish(event orchdomain.RuntimeEvent) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	listeners := s.subscribers[event.SessionID]
	for _, ch := range listeners {
		select {
		case ch <- event:
		default:
		}
	}
}

func (s *Service) unsubscribe(sessionID string, id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	listeners := s.subscribers[sessionID]
	if listeners == nil {
		return
	}

	if ch, ok := listeners[id]; ok {
		delete(listeners, id)
		close(ch)
	}

	if len(listeners) == 0 {
		delete(s.subscribers, sessionID)
	}
}

// Close marks the session as closed.
func (s *Service) Close(ctx context.Context, id string) error {
	logger := pkglog.FromContext(ctx)

	sess, err := s.sessions.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if sess.Status == session.StatusClosed {
		return nil // idempotent
	}
	sess.Status = session.StatusClosed
	sess.UpdatedAt = time.Now().UTC()
	if err := s.sessions.Update(ctx, sess); err != nil {
		logger.ErrorContext(ctx, "failed to close session", "session_id", id, "error", err)
		return err
	}

	logger.InfoContext(ctx, "session closed", "session_id", id)
	return nil
}

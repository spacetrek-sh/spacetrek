package sessionsvc

import (
	"context"
	"time"

	"github.com/kumori-sh/spacetrk/pkg/exception"
	"github.com/kumori-sh/spacetrk/src/core/domain/agent"
	"github.com/kumori-sh/spacetrk/src/core/domain/session"
)

// Service implements the session business logic.
type Service struct {
	sessions session.Repository
	agents   agent.Repository
}

func New(sessions session.Repository, agents agent.Repository) *Service {
	return &Service{sessions: sessions, agents: agents}
}

// Create opens a new session, verifying that the requested agent exists first.
func (s *Service) Create(ctx context.Context, p session.CreateParams) (*session.Session, error) {
	if _, err := s.agents.GetByID(ctx, p.AgentID); err != nil {
		return nil, err
	}
	sess := session.New(p)
	if err := s.sessions.Create(ctx, sess); err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *Service) Get(ctx context.Context, id string) (*session.Session, error) {
	return s.sessions.GetByID(ctx, id)
}

// SendMessage appends the user message to the session's history, invokes the
// LLM (stubbed until infrastructure is wired), and persists the result.
func (s *Service) SendMessage(ctx context.Context, id, content string) (*session.Session, error) {
	sess, err := s.sessions.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if sess.Status == session.StatusClosed {
		return nil, exception.BadRequest("session is already closed")
	}

	sess.AddMessage(session.RoleUser, content)

	// TODO: replace stub with real LLM invocation once the gateway is wired.
	sess.AddMessage(session.RoleAssistant, "[LLM gateway not yet connected]")

	if err := s.sessions.Update(ctx, sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// Close marks the session as closed.
func (s *Service) Close(ctx context.Context, id string) error {
	sess, err := s.sessions.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if sess.Status == session.StatusClosed {
		return nil // idempotent
	}
	sess.Status = session.StatusClosed
	sess.UpdatedAt = time.Now().UTC()
	return s.sessions.Update(ctx, sess)
}

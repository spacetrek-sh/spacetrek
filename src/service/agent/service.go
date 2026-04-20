package agentsvc

import (
	"context"
	"time"

	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/agent"
)

// Service implements the agent business logic.
type Service struct {
	repo agent.Repository
}

func New(repo agent.Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Create(ctx context.Context, p agent.CreateParams) (*agent.Agent, error) {
	logger := pkglog.FromContext(ctx)

	a := agent.New(p)
	if err := s.repo.Create(ctx, a); err != nil {
		logger.ErrorContext(ctx, "failed to persist agent", "name", p.Name, "error", err)
		return nil, err
	}

	logger.InfoContext(ctx, "agent created", "agent_id", a.ID, "name", a.Name)
	return a, nil
}

func (s *Service) Get(ctx context.Context, id string) (*agent.Agent, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *Service) List(ctx context.Context, offset, limit int) ([]*agent.Agent, int64, error) {
	return s.repo.List(ctx, offset, limit)
}

func (s *Service) Update(ctx context.Context, id string, p agent.UpdateParams) (*agent.Agent, error) {
	logger := pkglog.FromContext(ctx)

	a, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if p.Name != nil {
		a.Name = *p.Name
	}
	if p.Description != nil {
		a.Description = *p.Description
	}
	if p.Model != nil {
		a.Model = *p.Model
	}
	if p.SystemPrompt != nil {
		a.SystemPrompt = *p.SystemPrompt
	}
	a.UpdatedAt = time.Now().UTC()

	if err := s.repo.Update(ctx, a); err != nil {
		logger.ErrorContext(ctx, "failed to update agent", "agent_id", id, "error", err)
		return nil, err
	}

	logger.InfoContext(ctx, "agent updated", "agent_id", id)
	return a, nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	logger := pkglog.FromContext(ctx)

	if err := s.repo.Delete(ctx, id); err != nil {
		logger.WarnContext(ctx, "failed to delete agent", "agent_id", id, "error", err)
		return err
	}

	logger.InfoContext(ctx, "agent deleted", "agent_id", id)
	return nil
}

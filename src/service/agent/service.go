package agentsvc

import (
	"context"
	"time"

	"github.com/kumori-sh/spacetrk/src/core/domain/agent"
)

// Service implements the agent business logic.
type Service struct {
	repo agent.Repository
}

func New(repo agent.Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) Create(ctx context.Context, p agent.CreateParams) (*agent.Agent, error) {
	a := agent.New(p)
	if err := s.repo.Create(ctx, a); err != nil {
		return nil, err
	}
	return a, nil
}

func (s *Service) Get(ctx context.Context, id string) (*agent.Agent, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *Service) List(ctx context.Context, offset, limit int) ([]*agent.Agent, int64, error) {
	return s.repo.List(ctx, offset, limit)
}

func (s *Service) Update(ctx context.Context, id string, p agent.UpdateParams) (*agent.Agent, error) {
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
		return nil, err
	}
	return a, nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

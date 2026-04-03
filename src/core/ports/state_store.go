package ports

import (
	"context"

	orchdomain "github.com/kumori-sh/spacetrk/src/core/domain/orchestrator"
)

// ConversationStateStore persists orchestrator state by session.
type ConversationStateStore interface {
	Load(ctx context.Context, sessionID string) (orchdomain.State, error)
	Save(ctx context.Context, state orchdomain.State) error
}

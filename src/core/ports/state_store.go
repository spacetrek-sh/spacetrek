package ports

import (
	"context"

	orchdomain "github.com/spacetrek-sh/spacetrek/src/core/domain/orchestrator"
)

// ConversationStateStore persists orchestrator state by chat.
type ConversationStateStore interface {
	Load(ctx context.Context, chatID string) (orchdomain.State, error)
	Save(ctx context.Context, state orchdomain.State) error
}

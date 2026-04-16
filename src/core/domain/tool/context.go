package tool

import "context"

type chatIDKey struct{}

// WithChatID injects the chat ID into the context for downstream tools.
func WithChatID(ctx context.Context, chatID string) context.Context {
	return context.WithValue(ctx, chatIDKey{}, chatID)
}

// ChatIDFromContext extracts the chat ID injected by the orchestrator.
func ChatIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(chatIDKey{}).(string)
	return id, ok
}

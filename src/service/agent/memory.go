package agentsvc

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/spacetrek-sh/spacetrek/pkg/exception"
	pkglog "github.com/spacetrek-sh/spacetrek/pkg/log"
	"github.com/spacetrek-sh/spacetrek/src/core/domain/agent"
)

const (
	memoryMaxValueBytes = 4 * 1024       // 4 KB
	memoryMaxTTLSeconds = 24 * 60 * 60   // 24 hours
	memoryKeyMaxLen     = 64
)

var memoryKeyPattern = regexp.MustCompile(`^[a-z0-9_:-]{1,64}$`)

// ValidateMemoryKey reports whether key matches the chat-memory key schema
// ([a-z0-9_:-]{1,64}). Exported so tools can fail fast before round-tripping
// to the service.
func ValidateMemoryKey(key string) bool {
	return memoryKeyPattern.MatchString(key)
}

// MemoryService wraps agent.MemoryRepository with chat-scoped validation
// and TTL handling. The tool layer depends on this — never the repo — so
// storage concerns stay behind the service boundary.
type MemoryService struct {
	repo agent.MemoryRepository
}

func NewMemoryService(repo agent.MemoryRepository) *MemoryService {
	return &MemoryService{repo: repo}
}

// Set stores value under (chatID, key). ttlSeconds <= 0 means no expiry.
func (s *MemoryService) Set(ctx context.Context, chatID, key, value string, ttlSeconds int) error {
	logger := pkglog.FromContext(ctx)

	if !ValidateMemoryKey(key) {
		return exception.BadRequest(fmt.Sprintf("invalid memory key %q: must match [a-z0-9_:-]{1,%d}", key, memoryKeyMaxLen))
	}
	if len(value) > memoryMaxValueBytes {
		return exception.BadRequest(fmt.Sprintf("memory value exceeds %d-byte limit", memoryMaxValueBytes))
	}
	if ttlSeconds < 0 || ttlSeconds > memoryMaxTTLSeconds {
		return exception.BadRequest(fmt.Sprintf("ttl_seconds must be between 0 and %d", memoryMaxTTLSeconds))
	}

	entry := &agent.MemoryEntry{
		ChatID: chatID,
		Key:    key,
		Value:  value,
	}
	if ttlSeconds > 0 {
		entry.ExpiresAt = time.Now().UTC().Add(time.Duration(ttlSeconds) * time.Second)
	}

	if err := s.repo.Set(ctx, entry); err != nil {
		logger.ErrorContext(ctx, "memory service: set failed",
			"chat_id", chatID, "key", key, "error", err)
		return err
	}
	return nil
}

// Get returns the stored value. The found flag is false when the key is
// missing or expired — never an error in that case, so callers can render
// the LLM-friendly {"value": ""} payload without branching on error type.
func (s *MemoryService) Get(ctx context.Context, chatID, key string) (value string, found bool, err error) {
	entry, err := s.repo.Get(ctx, chatID, key)
	if err != nil {
		var appErr *exception.AppError
		if errors.As(err, &appErr) && appErr.Code == exception.CodeNotFound {
			return "", false, nil
		}
		logger := pkglog.FromContext(ctx)
		logger.ErrorContext(ctx, "memory service: get failed",
			"chat_id", chatID, "key", key, "error", err)
		return "", false, err
	}
	return entry.Value, true, nil
}

// Delete removes a key. Returns a NotFound error if the key is absent so
// the tool can surface that to the LLM.
func (s *MemoryService) Delete(ctx context.Context, chatID, key string) error {
	if err := s.repo.Delete(ctx, chatID, key); err != nil {
		logger := pkglog.FromContext(ctx)
		logger.WarnContext(ctx, "memory service: delete failed",
			"chat_id", chatID, "key", key, "error", err)
		return err
	}
	return nil
}

// List returns all non-expired entries for the chat.
func (s *MemoryService) List(ctx context.Context, chatID string) ([]*agent.MemoryEntry, error) {
	return s.repo.List(ctx, chatID)
}

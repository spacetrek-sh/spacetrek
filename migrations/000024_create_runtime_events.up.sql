-- Runtime event type enum (mirrors orchestrator.RuntimeEventType).
CREATE TYPE runtime_event_type AS ENUM (
    'token',
    'thinking',
    'answer',
    'tool_call',
    'error',
    'done'
);

-- Runtime events are persisted from the orchestrator SSE stream.
-- Each row is one event emitted during a chat turn.
CREATE TABLE runtime_events (
    id          UUID                PRIMARY KEY DEFAULT gen_random_uuid(),
    chat_id     UUID                NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    trace_id    VARCHAR,
    type        runtime_event_type  NOT NULL,
    step        INTEGER             NOT NULL DEFAULT 0,
    data        TEXT                NOT NULL DEFAULT '',
    command     TEXT                NOT NULL DEFAULT '',
    result      TEXT                NOT NULL DEFAULT '',
    error       TEXT                NOT NULL DEFAULT '',
    token_usage JSONB,
    metadata    JSONB,
    created_at  TIMESTAMPTZ         NOT NULL DEFAULT NOW()
);

-- Primary access: load events for a chat chronologically.
CREATE INDEX idx_runtime_events_chat_created ON runtime_events (chat_id, created_at ASC);

-- Filter by event type within a chat.
CREATE INDEX idx_runtime_events_chat_type     ON runtime_events (chat_id, type);

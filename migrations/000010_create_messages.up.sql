CREATE TABLE messages (
    id              UUID                 PRIMARY KEY DEFAULT gen_random_uuid(),
    chat_id         UUID                 NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    role            message_role         NOT NULL,
    content_type    message_content_type NOT NULL,
    content_body    TEXT                 NOT NULL,
    sequence_number BIGSERIAL            NOT NULL,
    tool_call_id    VARCHAR,
    metadata        JSONB,
    created_at      TIMESTAMPTZ          NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ,

    CONSTRAINT messages_sequence_number_unique UNIQUE (sequence_number)
);

-- Hot path: message history + LATERAL last-message lookup
CREATE INDEX idx_messages_chat_seq     ON messages (chat_id, sequence_number DESC);

-- Active messages only (excludes soft-deleted)
CREATE INDEX idx_messages_chat_active  ON messages (chat_id, sequence_number DESC)
    WHERE deleted_at IS NULL;

-- Correlate tool_call ↔ tool_result turn pairs
CREATE INDEX idx_messages_tool_call_id ON messages (tool_call_id)
    WHERE tool_call_id IS NOT NULL;

-- Retrieve messages by role within a chat (e.g. system prompt fetch)
CREATE INDEX idx_messages_chat_role    ON messages (chat_id, role);

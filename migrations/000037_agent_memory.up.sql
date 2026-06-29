-- Agent self-memory: chat-scoped key/value scratchpad that survives VM
-- snapshot/resume cycles within the same chat. Keyed on (chat_id, key) so
-- memory never leaks across chats. Wall-clock TTL — see
-- docs/issues/snapshot-resume-time-sync.md for the resume drift tradeoff.
CREATE TABLE agent_memory (
    chat_id    UUID         NOT NULL,
    key        TEXT         NOT NULL,
    value      TEXT         NOT NULL,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    PRIMARY KEY (chat_id, key)
);

CREATE INDEX agent_memory_expires_at_idx ON agent_memory (expires_at)
    WHERE expires_at IS NOT NULL;

COMMENT ON TABLE agent_memory IS
    'Chat-scoped key/value scratchpad surfaced via the memory.* LLM tools.';

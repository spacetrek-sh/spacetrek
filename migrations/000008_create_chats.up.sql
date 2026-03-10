CREATE TABLE chats (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    agent_id   UUID        NOT NULL REFERENCES agents (id),
    vm_id      UUID        REFERENCES vm_instances (id) ON DELETE SET NULL,
    title      VARCHAR     NOT NULL,
    status     chat_status NOT NULL DEFAULT 'active',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX idx_chats_user_id    ON chats (user_id, created_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_chats_user_status ON chats (user_id, status)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_chats_agent_id   ON chats (agent_id)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_chats_vm_id      ON chats (vm_id)
    WHERE vm_id IS NOT NULL;

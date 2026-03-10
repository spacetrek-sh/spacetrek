CREATE TABLE agents (
    id            UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID         NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name          VARCHAR      NOT NULL,
    description   TEXT,
    model         VARCHAR      NOT NULL,
    system_prompt TEXT,
    config        JSONB        NOT NULL DEFAULT '{}',
    status        agent_status NOT NULL DEFAULT 'created',
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at    TIMESTAMPTZ
);

CREATE INDEX idx_agents_user_id ON agents (user_id, created_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_agents_status  ON agents (status)
    WHERE deleted_at IS NULL;

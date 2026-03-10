CREATE TABLE access_keys (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name         VARCHAR     NOT NULL,
    key_prefix   VARCHAR     NOT NULL,
    key_hash     VARCHAR     NOT NULL,
    scopes       JSONB       NOT NULL DEFAULT '[]',
    last_used_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,
    is_active    BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Auth middleware hot path: lookup active key by hash
CREATE UNIQUE INDEX idx_access_keys_hash_active ON access_keys (key_hash)
    WHERE is_active = TRUE;

CREATE INDEX idx_access_keys_user_id    ON access_keys (user_id, created_at DESC)
    WHERE is_active = TRUE;

CREATE INDEX idx_access_keys_expires_at ON access_keys (expires_at)
    WHERE is_active = TRUE AND expires_at IS NOT NULL;

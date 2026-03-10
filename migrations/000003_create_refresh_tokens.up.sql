CREATE TABLE refresh_tokens (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash  VARCHAR     NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_refresh_tokens_hash     ON refresh_tokens (token_hash);
CREATE INDEX        idx_refresh_tokens_user_id  ON refresh_tokens (user_id);
CREATE INDEX        idx_refresh_tokens_expires  ON refresh_tokens (expires_at)
    WHERE revoked_at IS NULL;

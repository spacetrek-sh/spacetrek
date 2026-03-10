CREATE TABLE users (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    username      VARCHAR     NOT NULL,
    email         VARCHAR     NOT NULL,
    password_hash VARCHAR     NOT NULL,
    role          user_role   NOT NULL DEFAULT 'user',
    is_verified   BOOLEAN     NOT NULL DEFAULT FALSE,
    verified_at   TIMESTAMPTZ,
    last_login_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at    TIMESTAMPTZ
);

-- Partial unique indexes exclude soft-deleted rows
CREATE UNIQUE INDEX idx_users_email    ON users (email)    WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX idx_users_username ON users (username) WHERE deleted_at IS NULL;
CREATE INDEX idx_users_created_at      ON users (created_at DESC) WHERE deleted_at IS NULL;

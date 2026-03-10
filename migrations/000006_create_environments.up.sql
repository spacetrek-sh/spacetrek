CREATE TABLE environments (
    id              UUID             PRIMARY KEY DEFAULT gen_random_uuid(),
    type            environment_type NOT NULL,
    image_path      VARCHAR          NOT NULL,
    resource_limits JSONB            NOT NULL,
    metadata        JSONB,
    created_at      TIMESTAMPTZ      NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ      NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_environments_type ON environments (type);

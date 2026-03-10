-- audit_logs is append-only: no updated_at, no deleted_at, never UPDATE or DELETE.
CREATE TABLE audit_logs (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        UUID,   -- intentionally no FK: users may be deleted but logs must survive
    request_id     VARCHAR,
    correlation_id VARCHAR,
    action         VARCHAR     NOT NULL,
    resource_type  VARCHAR     NOT NULL,
    resource_id    UUID,
    ip_address     VARCHAR     NOT NULL,
    user_agent     VARCHAR,
    metadata       JSONB,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- User activity timeline (admin / compliance)
CREATE INDEX idx_audit_logs_user_id        ON audit_logs (user_id, created_at DESC)
    WHERE user_id IS NOT NULL;

-- All events for a specific resource
CREATE INDEX idx_audit_logs_resource       ON audit_logs (resource_type, resource_id, created_at DESC);

-- Filter by action type across time
CREATE INDEX idx_audit_logs_action         ON audit_logs (action, created_at DESC);

-- Trace all DB events from a single HTTP request
CREATE INDEX idx_audit_logs_request_id     ON audit_logs (request_id)
    WHERE request_id IS NOT NULL;

-- Cross-service distributed trace
CREATE INDEX idx_audit_logs_correlation_id ON audit_logs (correlation_id)
    WHERE correlation_id IS NOT NULL;

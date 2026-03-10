-- chat_id is intentionally omitted here — chats does not exist yet.
-- It is added in migration 000009 after chats is created.
CREATE TABLE vm_instances (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    environment_id UUID        NOT NULL REFERENCES environments (id),
    provider       vm_provider NOT NULL,
    status         vm_status   NOT NULL DEFAULT 'provisioning',
    ip_address     VARCHAR,
    assigned_at    TIMESTAMPTZ,
    terminated_at  TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Pool query: unassigned ready VMs by provider
CREATE INDEX idx_vm_instances_pool         ON vm_instances (provider, status)
    WHERE status != 'terminated';

CREATE INDEX idx_vm_instances_environment  ON vm_instances (environment_id);

CREATE INDEX idx_vm_instances_status_active ON vm_instances (status, created_at)
    WHERE status != 'terminated';

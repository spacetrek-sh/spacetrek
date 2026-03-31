CREATE TABLE vm_leases (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    chat_id UUID NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    vm_id UUID NOT NULL REFERENCES vm_instances (id) ON DELETE CASCADE,
    leased_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    released_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX ux_vm_leases_active_vm
    ON vm_leases (vm_id)
    WHERE released_at IS NULL;

CREATE UNIQUE INDEX ux_vm_leases_active_chat_vm
    ON vm_leases (chat_id, vm_id)
    WHERE released_at IS NULL;

CREATE INDEX idx_vm_leases_chat_active
    ON vm_leases (chat_id, leased_at DESC)
    WHERE released_at IS NULL;

CREATE INDEX idx_vm_leases_vm_active
    ON vm_leases (vm_id, leased_at DESC)
    WHERE released_at IS NULL;

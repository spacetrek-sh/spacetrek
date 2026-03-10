CREATE TABLE vm_volumes (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id       UUID        NOT NULL REFERENCES vm_instances (id) ON DELETE CASCADE,
    type        volume_type NOT NULL,
    source      VARCHAR     NOT NULL,
    mount_path  VARCHAR     NOT NULL,
    is_readonly BOOLEAN     NOT NULL DEFAULT FALSE,
    metadata    JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_vm_volumes_vm_id ON vm_volumes (vm_id);

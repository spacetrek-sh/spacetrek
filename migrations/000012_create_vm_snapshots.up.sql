CREATE TABLE vm_snapshots (
    id                 UUID          PRIMARY KEY DEFAULT gen_random_uuid(),
    vm_id              UUID          NOT NULL REFERENCES vm_instances (id) ON DELETE CASCADE,
    parent_snapshot_id UUID          REFERENCES vm_snapshots (id) ON DELETE RESTRICT,
    type               snapshot_type NOT NULL,
    snapshot_path      VARCHAR       NOT NULL,
    size_bytes         BIGINT        NOT NULL,
    metadata           JSONB,
    created_at         TIMESTAMPTZ   NOT NULL DEFAULT NOW()
);

-- Walk restore chain newest-first
CREATE INDEX idx_vm_snapshots_vm_id    ON vm_snapshots (vm_id, created_at DESC);

-- Walk parent chain upward (incremental → full)
CREATE INDEX idx_vm_snapshots_parent   ON vm_snapshots (parent_snapshot_id)
    WHERE parent_snapshot_id IS NOT NULL;

-- Find latest full (base) snapshot for a VM
CREATE INDEX idx_vm_snapshots_vm_type  ON vm_snapshots (vm_id, type);

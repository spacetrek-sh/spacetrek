CREATE TABLE snapshot_metrics (
    id BIGSERIAL PRIMARY KEY,
    snapshot_id UUID NOT NULL REFERENCES vm_snapshots (id) ON DELETE CASCADE,
    vm_id UUID NOT NULL REFERENCES vm_instances (id) ON DELETE CASCADE,
    type VARCHAR(20) NOT NULL,

    -- Creation metrics
    pause_duration_ms  BIGINT NOT NULL DEFAULT 0,
    memory_bytes       BIGINT NOT NULL DEFAULT 0,
    memory_zst_bytes   BIGINT NOT NULL DEFAULT 0,
    cow_bytes          BIGINT NOT NULL DEFAULT 0,
    cow_zst_bytes      BIGINT NOT NULL DEFAULT 0,
    upload_duration_ms BIGINT NOT NULL DEFAULT 0,

    -- Resume metrics
    download_duration_ms   BIGINT NOT NULL DEFAULT 0,
    decompress_duration_ms BIGINT NOT NULL DEFAULT 0,
    restore_duration_ms    BIGINT NOT NULL DEFAULT 0,
    agent_ready_ms         BIGINT NOT NULL DEFAULT 0,
    total_resume_ms        BIGINT NOT NULL DEFAULT 0,

    -- Context
    guest_ram_mb   INT    NOT NULL DEFAULT 0,
    workload_type  VARCHAR(20) NOT NULL DEFAULT 'idle',
    chain_depth    INT    NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_snapshot_metrics_vm ON snapshot_metrics (vm_id, created_at DESC);
CREATE INDEX idx_snapshot_metrics_type ON snapshot_metrics (type, created_at DESC);

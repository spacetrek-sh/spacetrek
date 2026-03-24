CREATE TABLE vm_metrics_history (
    id BIGSERIAL PRIMARY KEY,
    vm_id UUID NOT NULL REFERENCES vm_instances (id) ON DELETE CASCADE,
    collected_at TIMESTAMPTZ NOT NULL,
    cpu_usage_percent DOUBLE PRECISION NOT NULL DEFAULT 0,
    memory_used_mb INT NOT NULL DEFAULT 0,
    memory_limit_mb INT NOT NULL DEFAULT 0,
    memory_percent DOUBLE PRECISION NOT NULL DEFAULT 0,
    disk_used_mb INT NOT NULL DEFAULT 0,
    disk_limit_mb INT NOT NULL DEFAULT 0,
    disk_percent DOUBLE PRECISION NOT NULL DEFAULT 0,
    network_bytes_sent BIGINT NOT NULL DEFAULT 0,
    network_bytes_received BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_vm_metrics_history_vm_time
    ON vm_metrics_history (vm_id, collected_at DESC);

CREATE INDEX idx_vm_metrics_history_collected_at
    ON vm_metrics_history (collected_at DESC);

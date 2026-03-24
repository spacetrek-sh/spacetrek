-- Add runtime metadata columns to vm_instances.
-- These fields allow runtime reconciliation and lifecycle workers.

ALTER TABLE vm_instances
    ADD COLUMN runtime_id VARCHAR,
    ADD COLUMN socket_path VARCHAR,
    ADD COLUMN pid INT,
    ADD COLUMN runtime_state_source VARCHAR,
    ADD COLUMN last_heartbeat_at TIMESTAMPTZ,
    ADD COLUMN idle_deadline_at TIMESTAMPTZ;

-- Fast lookup by runtime identity.
CREATE UNIQUE INDEX idx_vm_instances_runtime_id
    ON vm_instances (runtime_id)
    WHERE runtime_id IS NOT NULL;

-- Efficient scans for idle timeout workers.
CREATE INDEX idx_vm_instances_idle_deadline
    ON vm_instances (idle_deadline_at)
    WHERE idle_deadline_at IS NOT NULL AND status IN ('ready', 'running', 'idle');

COMMENT ON COLUMN vm_instances.runtime_id IS 'Provider runtime identifier for VM instance tracking.';
COMMENT ON COLUMN vm_instances.socket_path IS 'Firecracker API socket path for the runtime.';
COMMENT ON COLUMN vm_instances.pid IS 'Runtime process id on the host.';
COMMENT ON COLUMN vm_instances.runtime_state_source IS 'Last runtime-observed state from backend provider.';
COMMENT ON COLUMN vm_instances.last_heartbeat_at IS 'Last time runtime status was observed healthy.';
COMMENT ON COLUMN vm_instances.idle_deadline_at IS 'When VM should be auto-stopped due to inactivity.';
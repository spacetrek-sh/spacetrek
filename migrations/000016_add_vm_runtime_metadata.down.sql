DROP INDEX IF EXISTS idx_vm_instances_idle_deadline;
DROP INDEX IF EXISTS idx_vm_instances_runtime_id;

ALTER TABLE vm_instances
    DROP COLUMN IF EXISTS idle_deadline_at,
    DROP COLUMN IF EXISTS last_heartbeat_at,
    DROP COLUMN IF EXISTS runtime_state_source,
    DROP COLUMN IF EXISTS pid,
    DROP COLUMN IF EXISTS socket_path,
    DROP COLUMN IF EXISTS runtime_id;
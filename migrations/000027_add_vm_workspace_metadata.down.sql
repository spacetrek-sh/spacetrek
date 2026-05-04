DROP INDEX IF EXISTS idx_vm_instances_conversation_id;

ALTER TABLE vm_instances
    DROP COLUMN IF EXISTS workspace_size_gb,
    DROP COLUMN IF EXISTS conversation_id;

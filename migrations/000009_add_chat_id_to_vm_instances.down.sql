DROP INDEX IF EXISTS idx_vm_instances_chat_id;

ALTER TABLE vm_instances
    DROP COLUMN IF EXISTS chat_id;

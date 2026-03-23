-- Remove resource override columns from vm_instances
ALTER TABLE vm_instances
    DROP COLUMN IF EXISTS vcpu,
    DROP COLUMN IF EXISTS memory_mb,
    DROP COLUMN IF EXISTS disk_mb;

-- Add resource override columns to vm_instances
-- These columns allow per-VM customization while keeping environment as default.
-- NULL values mean "use the environment default".

ALTER TABLE vm_instances
    ADD COLUMN vcpu INT,
    ADD COLUMN memory_mb INT,
    ADD COLUMN disk_mb INT;

-- Add comment for documentation
COMMENT ON COLUMN vm_instances.vcpu IS 'Optional vCPU override. NULL = use environment default.';
COMMENT ON COLUMN vm_instances.memory_mb IS 'Optional memory override in MB. NULL = use environment default.';
COMMENT ON COLUMN vm_instances.disk_mb IS 'Optional disk size override in MB. NULL = use environment default.';

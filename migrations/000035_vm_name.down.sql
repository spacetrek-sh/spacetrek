ALTER TABLE vm_instances
    DROP CONSTRAINT IF EXISTS vm_instances_name_unique;

ALTER TABLE vm_instances DROP COLUMN IF EXISTS name;

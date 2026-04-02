DROP INDEX IF EXISTS idx_vm_instances_vsock_path_active;
DROP INDEX IF EXISTS idx_vm_instances_guest_cid_active;

ALTER TABLE vm_instances
    DROP CONSTRAINT IF EXISTS chk_vm_instances_guest_cid_range;

ALTER TABLE vm_instances
    DROP COLUMN IF EXISTS guest_cid,
    DROP COLUMN IF EXISTS vsock_path;

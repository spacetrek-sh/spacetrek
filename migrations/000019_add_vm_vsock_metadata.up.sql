-- Persist host-guest vsock addressing metadata per VM.
ALTER TABLE vm_instances
    ADD COLUMN vsock_path VARCHAR,
    ADD COLUMN guest_cid BIGINT;

ALTER TABLE vm_instances
    ADD CONSTRAINT chk_vm_instances_guest_cid_range
    CHECK (guest_cid IS NULL OR (guest_cid >= 3 AND guest_cid <= 4294967295));

-- Keep active-runtime CID/path allocations unique across VM records.
CREATE UNIQUE INDEX idx_vm_instances_guest_cid_active
    ON vm_instances (guest_cid)
    WHERE guest_cid IS NOT NULL
      AND status IN ('provisioning', 'ready', 'running', 'idle');

CREATE UNIQUE INDEX idx_vm_instances_vsock_path_active
    ON vm_instances (vsock_path)
    WHERE vsock_path IS NOT NULL
      AND status IN ('provisioning', 'ready', 'running', 'idle');

COMMENT ON COLUMN vm_instances.vsock_path IS 'Host-side AF_UNIX path used for Firecracker vsock communication.';
COMMENT ON COLUMN vm_instances.guest_cid IS 'Guest CID allocated for Firecracker vsock device (uint32 range).';

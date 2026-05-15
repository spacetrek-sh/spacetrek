ALTER TABLE vm_instances
    ADD COLUMN diff_snapshots_enabled BOOLEAN NOT NULL DEFAULT false;

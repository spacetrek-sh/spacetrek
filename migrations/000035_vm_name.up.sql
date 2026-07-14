-- VM DNS naming: adds a unique secondary name column to vm_instances.
-- The UUID id remains the canonical primary key; name is a human-readable
-- handle for DNS resolution, logs, and the dashboard.
--
-- Phased (nullable → backfill → NOT NULL + UNIQUE) so existing rows get a
-- stable legacy-<uuid8> name before the constraint is applied.

ALTER TABLE vm_instances ADD COLUMN name TEXT;

UPDATE vm_instances
   SET name = 'legacy-' || substring(id::text, 1, 8)
 WHERE name IS NULL;

ALTER TABLE vm_instances
    ALTER COLUMN name SET NOT NULL,
    ADD CONSTRAINT vm_instances_name_unique UNIQUE (name);

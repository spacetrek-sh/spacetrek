CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_runtime_events_created_at_desc
    ON runtime_events (created_at DESC);

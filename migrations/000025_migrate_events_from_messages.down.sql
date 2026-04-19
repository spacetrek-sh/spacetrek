-- Move events back into messages (lossy: enriched fields stay in metadata JSON).
INSERT INTO messages (id, chat_id, role, content_type, content_body, metadata, sequence_number, created_at)
SELECT
    re.id,
    re.chat_id,
    re.type::text,
    'text',
    re.data,
    COALESCE(re.metadata, '{}'::jsonb),
    0,
    re.created_at
FROM runtime_events re
WHERE NOT EXISTS (
    SELECT 1 FROM messages m WHERE m.id = re.id
);

TRUNCATE runtime_events;

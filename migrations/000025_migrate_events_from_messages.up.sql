-- Move runtime-event rows from messages into runtime_events.
-- Maps old message_role values to canonical runtime_event_type values.
INSERT INTO runtime_events (id, chat_id, trace_id, type, step, data, command, result, error, token_usage, metadata, created_at)
SELECT
    m.id,
    m.chat_id,
    m.metadata->>'trace_id'                           AS trace_id,
    CASE m.role
        WHEN 'thinking'          THEN 'thinking'::runtime_event_type
        WHEN 'answer'            THEN 'answer'
        WHEN 'token'             THEN 'token'
        WHEN 'tool_call'         THEN 'tool_call'
        WHEN 'error'             THEN 'error'
        WHEN 'done'              THEN 'done'
        WHEN 'llm_thinking'      THEN 'thinking'
        WHEN 'llm_answer'        THEN 'answer'
        WHEN 'llm_token'         THEN 'token'
        WHEN 'execution_summary' THEN 'done'
        WHEN 'processing_done'   THEN 'done'
        WHEN 'agent_error'       THEN 'error'
        WHEN 'tool_start'        THEN 'tool_call'
        WHEN 'tool_stdout'       THEN 'tool_call'
        WHEN 'tool_end'          THEN 'tool_call'
        ELSE 'error'
    END,
    COALESCE((m.metadata->>'step')::int, 0),
    m.content_body,
    COALESCE(m.metadata->>'command', ''),
    COALESCE(m.metadata->>'result', ''),
    COALESCE(m.metadata->>'error', ''),
    m.metadata->'token_usage',
    m.metadata,
    m.created_at
FROM messages m
WHERE m.role NOT IN ('user', 'assistant', 'system', 'tool');

-- Remove migrated rows from messages.
DELETE FROM messages
WHERE role NOT IN ('user', 'assistant', 'system', 'tool');

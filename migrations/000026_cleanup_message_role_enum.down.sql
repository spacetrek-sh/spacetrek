ALTER TYPE message_role RENAME TO message_role_old;
CREATE TYPE message_role AS ENUM (
    'user', 'assistant', 'system', 'tool',
    'tool_start', 'tool_stdout', 'tool_end',
    'llm_thinking', 'llm_answer', 'llm_token',
    'execution_summary', 'processing_done', 'agent_error',
    'thinking', 'answer', 'token', 'tool_call', 'error', 'done'
);
ALTER TABLE messages ALTER COLUMN role TYPE message_role USING role::text::message_role;
DROP TYPE message_role_old;

ALTER TYPE message_role ADD VALUE IF NOT EXISTS 'thinking';
ALTER TYPE message_role ADD VALUE IF NOT EXISTS 'answer';
ALTER TYPE message_role ADD VALUE IF NOT EXISTS 'token';
ALTER TYPE message_role ADD VALUE IF NOT EXISTS 'tool_call';
ALTER TYPE message_role ADD VALUE IF NOT EXISTS 'error';
ALTER TYPE message_role ADD VALUE IF NOT EXISTS 'done';

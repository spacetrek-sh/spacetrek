-- PostgreSQL does not support removing values from an enum.
-- To roll back, the enum type must be recreated without the new values.
-- This is destructive and should only be run if no rows reference the new values.
ALTER TYPE message_role RENAME TO message_role_old;
CREATE TYPE message_role AS ENUM ('user', 'assistant', 'system', 'tool');
ALTER TABLE messages ALTER COLUMN role TYPE message_role USING role::text::message_role;
DROP TYPE message_role_old;

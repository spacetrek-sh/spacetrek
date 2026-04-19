-- Remove runtime event values from message_role enum.
ALTER TYPE message_role RENAME TO message_role_old;
CREATE TYPE message_role AS ENUM ('user', 'assistant', 'system', 'tool');
ALTER TABLE messages ALTER COLUMN role TYPE message_role USING role::text::message_role;
DROP TYPE message_role_old;

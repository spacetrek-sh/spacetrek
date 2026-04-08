-- System user for auto-created agents and anonymous chat sessions.
INSERT INTO users (id, username, email, password_hash, role, is_verified)
VALUES (
    '00000000-0000-0000-0000-000000000000',
    'system',
    'system@spacetrk.internal',
    '',
    'admin',
    TRUE
) ON CONFLICT (id) DO NOTHING;

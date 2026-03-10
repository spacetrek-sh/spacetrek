-- Resolves the circular FK: vm_instances <-> chats
-- chats now exists (000008), so we can safely add the FK back to it.
ALTER TABLE vm_instances
    ADD COLUMN  chat_id UUID REFERENCES chats (id) ON DELETE SET NULL;

CREATE INDEX idx_vm_instances_chat_id ON vm_instances (chat_id)
    WHERE chat_id IS NOT NULL;

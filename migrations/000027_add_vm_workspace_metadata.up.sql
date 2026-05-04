ALTER TABLE vm_instances
    ADD COLUMN conversation_id UUID REFERENCES chats (id) ON DELETE SET NULL,
    ADD COLUMN workspace_size_gb INTEGER NOT NULL DEFAULT 2;

CREATE INDEX idx_vm_instances_conversation_id ON vm_instances (conversation_id)
    WHERE conversation_id IS NOT NULL;
CREATE TABLE tasks (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    chat_id        UUID        NOT NULL REFERENCES chats (id) ON DELETE CASCADE,
    message_id     UUID        REFERENCES messages (id) ON DELETE SET NULL,
    vm_id          UUID        REFERENCES vm_instances (id) ON DELETE SET NULL,
    user_id        UUID        NOT NULL REFERENCES users (id),
    request_id     VARCHAR,
    correlation_id VARCHAR,
    type           task_type   NOT NULL,
    status         task_status NOT NULL DEFAULT 'pending',
    payload        JSONB       NOT NULL,
    result         JSONB,
    input_tokens   INTEGER,
    output_tokens  INTEGER,
    model          VARCHAR,
    started_at     TIMESTAMPTZ,
    finished_at    TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_tasks_chat_id        ON tasks (chat_id, created_at DESC);
CREATE INDEX idx_tasks_user_id        ON tasks (user_id, created_at DESC);

-- Worker queue: poll oldest pending tasks first
CREATE INDEX idx_tasks_pending        ON tasks (created_at ASC)
    WHERE status = 'pending';

-- Stuck task detection
CREATE INDEX idx_tasks_running        ON tasks (started_at ASC)
    WHERE status = 'running';

CREATE INDEX idx_tasks_vm_id          ON tasks (vm_id)
    WHERE vm_id IS NOT NULL;

CREATE INDEX idx_tasks_message_id     ON tasks (message_id)
    WHERE message_id IS NOT NULL;

-- Token cost reporting per user + model + time window
CREATE INDEX idx_tasks_cost_report    ON tasks (user_id, model, created_at)
    WHERE type = 'llm_inference';

-- Distributed trace lookups
CREATE INDEX idx_tasks_request_id     ON tasks (request_id)
    WHERE request_id IS NOT NULL;

CREATE INDEX idx_tasks_correlation_id ON tasks (correlation_id)
    WHERE correlation_id IS NOT NULL;

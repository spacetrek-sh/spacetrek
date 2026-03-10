-- User role
CREATE TYPE user_role AS ENUM ('admin', 'user');

-- Agent lifecycle
CREATE TYPE agent_status AS ENUM ('created', 'running', 'suspended', 'terminated');

-- Chat lifecycle
CREATE TYPE chat_status AS ENUM ('active', 'idle', 'closed');

-- Message author
CREATE TYPE message_role AS ENUM ('user', 'assistant', 'system', 'tool');

-- Message content format
CREATE TYPE message_content_type AS ENUM ('text', 'image', 'tool_call', 'tool_result');

-- Task classification
CREATE TYPE task_type AS ENUM ('llm_inference', 'tool_call', 'code_execution');

-- Task lifecycle
CREATE TYPE task_status AS ENUM ('pending', 'running', 'completed', 'failed', 'timeout');

-- VM instance lifecycle
CREATE TYPE vm_status AS ENUM ('provisioning', 'ready', 'running', 'idle', 'terminated');

-- VM provider backend
CREATE TYPE vm_provider AS ENUM ('firecracker', 'cloud-hypervisor');

-- VM snapshot type
CREATE TYPE snapshot_type AS ENUM ('full', 'incremental');

-- VM volume source type
CREATE TYPE volume_type AS ENUM ('local_dir', 'github_repo', 's3');

-- Environment base image type
CREATE TYPE environment_type AS ENUM ('alpine', 'python', 'node', 'ubuntu');

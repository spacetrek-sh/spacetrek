# spacetrek

A production-grade LLM orchestrator that gives AI agents access to isolated microVM environments (Firecracker) for secure code execution and stateful operations. Each agent conversation runs in its own sandboxed VM with resource limits, vsock-based command execution, and comprehensive observability.

## Features

- **MicroVM Isolation** — Firecracker VMs with CPU, memory, disk, and network limits per agent session
- **vsock Command Execution** — Framed JSON protocol over virtio-vsock for low-latency guest agent communication
- **ReAct Loop Orchestrator** — LLM iteratively selects and executes tools (VM commands) until final answer or step limit
- **VM Networking** — Point-to-point TAP routing with NAT, IP allocation, and nftables firewall
- **Snapshot & Restore** — S3-backed VM snapshots for state persistence across chat sessions
- **VM Resume** — Automatic VM reuse across conversations via lease tracking and snapshot restore
- **Real-Time Streaming** — SSE events for LLM thinking, answer tokens, and tool execution progress
- **Async Orchestration** — Messages processed asynchronously (202 Accepted) with runtime event streaming
- **JWT Authentication** — Access/refresh token pairs with role-based access control (admin/user)
- **Cursor Pagination** — Efficient cursor-based pagination for conversations and messages
- **Metrics & Monitoring** — CPU, memory, disk, network metrics sampled per VM with time-series history
- **Clean Architecture** — Hexagonal/Ports & Adapters with strict domain/infrastructure separation

## Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│                         API Layer                                │
│  REST (chi) + SSE streaming — Auth, Agent, Chat, VM handlers    │
└──────────────────────────────────────────────────────────────────┘
                              │
                    CORS → CorrelationID → RequestID
                         → Logging → Recovery → Auth
                              │
┌──────────────────────────────────────────────────────────────────┐
│                       Service Layer                              │
│                                                                  │
│  Chat Service ──── Orchestrator (ReAct Loop) ──── Tool Registry │
│       │                    │                          │          │
│  VM Resolver         LLM Gateway              VM Tools           │
│       │              (Gemini)          (create/start/execute/    │
│  Lease Tracking                           stop/snapshot)         │
└──────────────────────────────────────────────────────────────────┘
                              │
┌──────────────────────────────────────────────────────────────────┐
│                      Domain Layer                                │
│  Entities: Agent, Chat, VM, User, Tool, Environment, Snapshot   │
│  Ports: LLM, ToolRegistry, StateStore, SnapshotStore            │
└──────────────────────────────────────────────────────────────────┘
                              │
┌──────────────────────────────────────────────────────────────────┐
│                   Infrastructure Layer                           │
│  Firecracker Provider │ Gemini Client │ PostgreSQL │ S3/RustFS  │
│  (vsock, networking,  │ (tool calls,  │ (26 migrs) │ (snaps)    │
│   CID allocation)     │  streaming)   │            │            │
└──────────────────────────────────────────────────────────────────┘
```

## Project Structure

```
orchestrator/
├── cmd/
│   ├── main.go                 # Application entry point
│   └── seed/                   # Database seeder
├── pkg/                        # Reusable packages (no internal deps)
│   ├── auth/jwt/               # JWT token management
│   ├── config/                 # YAML config loading
│   ├── exception/              # Domain errors (AppError, FieldError)
│   ├── http/                   # HTTP response/request utilities
│   ├── json/                   # JSON helpers with AppError support
│   ├── grpc/                   # gRPC status conversion
│   ├── log/                    # Structured logger (tint/JSON)
│   └── validation/             # Struct validation
├── src/
│   ├── core/
│   │   ├── domain/             # Entities: agent, auth, chat, environment, orchestrator, snapshot, tool, user, vm, volume
│   │   └── ports/              # Interfaces: LLM, ToolRegistry, StateStore, SnapshotStore
│   ├── service/
│   │   ├── agent/              # Agent CRUD
│   │   ├── auth/               # Authentication, token management
│   │   ├── chat/               # Chat + async orchestration + VM resolver
│   │   ├── orchestrator/       # ReAct loop, tool planning, event streaming
│   │   ├── tool/               # VM tools (create, start, list, execute, stop, snapshot)
│   │   ├── user/               # User management
│   │   └── vm/                 # VM lifecycle, vsock exec, metrics, leases
│   ├── infrastructure/
│   │   ├── llm/gemini/         # Gemini API client with tool calling
│   │   ├── storage/s3/         # S3-compatible snapshot storage
│   │   └── vm/firecracker/     # Firecracker: lifecycle, vsock, networking, CID
│   ├── repository/
│   │   ├── postgres/           # PostgreSQL implementations
│   │   └── memory/             # In-memory fallback implementations
│   ├── api/http/
│   │   ├── server.go           # Router setup, middleware chain
│   │   └── v1/                 # Handlers: auth/, agent/, chat/, vm/
│   └── middleware/             # Auth, CORS, logging, recovery, request ID
├── migrations/                 # 26 PostgreSQL migrations
├── configs/                    # config.yaml + example
├── scripts/                    # Entrypoint and utility scripts
└── docs/                       # Architecture docs, ERD, OpenAPI spec
```

## Technology Stack

| Component | Technology |
|-----------|------------|
| Language | Go 1.24+ |
| Router | chi v5 |
| MicroVM | Firecracker (vsock guest agent) |
| Database | PostgreSQL |
| Storage | S3-compatible / RustFS |
| LLM | Gemini 3 Flash (extensible gateway) |
| Auth | JWT (access + refresh tokens) |
| Logging | slog (structured, tint for dev) |
| Metrics | Prometheus |
| Tracing | OpenTelemetry |

## Core Concepts

### Agent
LLM-powered agent with identity, model configuration, and system prompt. Lifecycle: **Created → Running → Suspended → Terminated**

### Chat
Conversation context between user and agent, bound to a microVM. Supports cursor-paginated message history and SSE streaming for real-time events. Replaces the "Session" concept.

### MicroVM
Isolated Firecracker VM with resource limits. Guest agent communicates over virtio-vsock using framed JSON protocol. Supports networking (TAP + NAT), snapshot/restore, and metrics collection.

### Environment
Pre-configured VM template (alpine, python, node, ubuntu) defining the rootfs image and default resource limits.

### Orchestrator (ReAct Loop)
The runtime engine that processes messages through an iterative loop: LLM generates response or selects tools → tools execute in VM → results feed back to LLM → repeat until final answer or max steps (10). Emits real-time SSE events for thinking, answer tokens, and tool execution.

### Tools
Agent capabilities registered in the tool registry and exposed to the LLM:
- **vm.create** — Create and assign a new VM to the chat
- **vm.start** — Resume a previously stopped VM
- **vm.list** — List VMs assigned to the chat
- **vm.execute_command** — Execute shell commands via vsock guest agent
- **vm.stop** — Stop and release a VM
- **vm.snapshot** — Create a VM snapshot for persistence

## Getting Started

### Prerequisites

- Go 1.24+
- PostgreSQL 14+
- Firecracker v1.5+ (Linux only)
- S3-compatible storage (RustFS recommended)

### Installation

```bash
# Clone the repository
git clone https://github.com/spacetrek-sh/spacetrek.git
cd spacetrek/orchestrator

# Install dependencies
go mod download

# Copy and configure environment
cp configs/config.yaml.example configs/config.yaml
# Edit configs/config.yaml with your settings

# Run database migrations
go run cmd/migrate/main.go up

# Start the server
go run cmd/main.go
```

### Configuration

Key configuration options in `configs/config.yaml`:

```yaml
environment: development

server:
  http_addr: ":8080"

database:
  url: postgresql://spacetrk:password@db:5432/spacetrk?sslmode=disable

vm:
  provider: firecracker
  cpu_count: 2
  memory_mb: 512
  disk_mb: 2048
  network_enabled: true
  firecracker:
    binary_path: firecracker
    kernel_path: /usr/share/firecracker/vmlinux
    network:
      subnet: 10.200.0.0/16
      enable_nat: true

llm:
  default_provider: gemini
  gemini:
    api_key: ${GEMINI_API_KEY}
    model: gemini-3-flash-preview

storage:
  endpoint: "http://rustfs:9000"
  bucket: "spacetrk-snapshots"

security:
  jwt_secret: ${JWT_SECRET}
  access_token_expiry: 1h
  refresh_token_expiry: 720h
```

## API Usage

### Authentication

```bash
# Register
curl -X POST http://localhost:8080/api/v1/auth/register \
  -H "Content-Type: application/json" \
  -d '{"username": "user", "email": "user@example.com", "password": "secret"}'

# Login
curl -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email": "user@example.com", "password": "secret"}'
```

### Create an Agent

```bash
curl -X POST http://localhost:8080/api/v1/agents \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Code Assistant",
    "model": "gemini-3-flash-preview",
    "system_prompt": "You are a helpful coding assistant."
  }'
```

### Send a Message (Async)

Messages are processed asynchronously. The response includes a chat ID for streaming.

```bash
curl -X POST http://localhost:8080/api/v1/chat \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{
    "agent_id": "agent-id",
    "message": "Create a Python script that sorts a list"
  }'
# Returns 202 Accepted with { "chat_id": "...", "status": "processing" }
```

### Stream Runtime Events (SSE)

```javascript
const evtSource = new EventSource(
  'http://localhost:8080/api/v1/chat/{chat_id}/stream',
  { headers: { 'Authorization': 'Bearer <token>' } }
);

evtSource.addEventListener('thinking', (e) => {
  console.log('LLM thinking:', JSON.parse(e.data));
});

evtSource.addEventListener('answer', (e) => {
  console.log('Answer token:', JSON.parse(e.data));
});

evtSource.addEventListener('tool_start', (e) => {
  console.log('Tool executing:', JSON.parse(e.data));
});

evtSource.addEventListener('tool_end', (e) => {
  console.log('Tool result:', JSON.parse(e.data));
});
```

### VM Management (Admin Only)

```bash
# Create a VM
curl -X POST http://localhost:8080/api/v1/vm \
  -H "Authorization: Bearer <admin-token>" \
  -H "Content-Type: application/json" \
  -d '{"environment_id": "python-env-id"}'

# Execute a command in VM
curl -X POST http://localhost:8080/api/v1/vm/{vm_id}/execute \
  -H "Authorization: Bearer <admin-token>" \
  -H "Content-Type: application/json" \
  -d '{"command": "python3 -c \"print(2+2)\""}'

# Get VM metrics
curl http://localhost:8080/api/v1/vm/{vm_id}/metrics \
  -H "Authorization: Bearer <admin-token>"

# Stream all running VMs (SSE)
curl http://localhost:8080/api/v1/vm/runtimes/stream \
  -H "Authorization: Bearer <admin-token>"
```

## Development Guidelines

- Core domain (`src/core/domain/`) must remain infrastructure-agnostic
- Define interfaces in `src/core/ports/`, implement in `src/infrastructure/`
- Use repository pattern for all data access (PostgreSQL + in-memory)
- Implement all services in `src/service/` layer
- Keep `pkg/` for reusable utilities with no internal dependencies
- Keep runtime orchestration logic in service layer; handlers should stay thin
- Treat tool contracts as domain/port boundaries, not infrastructure-specific types
- All schema changes require paired up/down SQL migrations in `migrations/`

## Security Model

1. JWT authentication (access + refresh tokens)
2. Role-based access control (admin/user)
3. Tool permission checks
4. Resource limits (CPU, RAM, disk per VM)
5. VM network isolation (TAP + NAT, no internet by default)
6. vsock-only guest communication (no SSH required)
7. Execution timeout enforcement
8. Output size limits (5MB stdout/stderr)
9. Audit logging via structured slog

## License

Proprietary - see [LICENSE](LICENSE) for details

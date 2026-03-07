# LLM Orchestrator with MicroVM Integration

A secure, scalable orchestration system for LLM agents with isolated microVM environments for safe, stateful execution.

## Overview

This project provides a production-grade orchestrator that gives LLM agents access to isolated microVM environments (Firecracker, Cloud Hypervisor) for secure code execution and stateful operations. Each agent session runs in its own sandboxed VM with resource limits, permission-based tool access, and comprehensive observability.

## Features

- **Secure Isolation**: Firecracker microVMs with CPU, memory, disk, and network limits
- **Stateful Sessions**: Persistent agent environments bound to VM instances
- **Multi-LLM Support**: Gemini (primary), OpenAI, Anthropic, and local models via unified gateway
- **Permission-Based Tools**: Fine-grained access control for agent capabilities
- **Horizontal Scalability**: Stateless orchestrators with VM pool management
- **Observability**: Structured logging, Prometheus metrics, and OpenTelemetry tracing
- **Clean Architecture**: Hexagonal/Ports & Adapters with clear separation of concerns

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                      API Layer                               │
│  (REST/gRPC/WebSocket - User & Admin Interfaces)            │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│                   Middleware Layer                           │
│  (Auth, Rate Limiting, Logging, Metrics, Validation)        │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│                   Service Layer                              │
│  Agent Orchestrator | Session Manager | Executor Service    │
│  VM Pool Manager | LLM Gateway | Security Policy           │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│                   Core Domain Layer                          │
│  (Business Logic, Domain Models, Interfaces)                │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│                Infrastructure Layer                          │
│  MicroVM Provider | LLM Client | Database | Storage | Queue │
└─────────────────────────────────────────────────────────────┘
```

## Project Structure

```
orchestrator/
├── cmd/                    # Application entry point, migrations, workers
├── pkg/                    # Reusable packages (no internal dependencies)
│   ├── exception/          # Domain errors and error code constructors
│   ├── http/               # HTTP response/request utilities
│   ├── json/               # JSON marshaling with AppError support
│   ├── grpc/               # gRPC utilities and status conversion
│   ├── log/                # Structured logger (tint/JSON slog handler)
│   └── validation/         # Struct validation with go-playground/validator
├── src/
│   ├── core/               # Domain layer (business logic, domain models)
│   │   ├── domain/         # Entities: agent, session, vm, task, tool
│   │   └── ports/          # Interface definitions (hexagonal arch)
│   ├── service/            # Business logic / Use cases
│   ├── infrastructure/     # External integrations (vm, llm, storage, queue)
│   ├── repository/         # Data persistence (postgres, in-memory)
│   ├── api/                # HTTP/gRPC handlers
│   └── middleware/         # HTTP middleware (auth, ratelimit, logging)
├── migrations/             # Database schema migrations
├── configs/                # Configuration files
├── scripts/                # Utility scripts
└── tests/                  # Integration and E2E tests
```

## Technology Stack

| Component | Technology |
|-----------|------------|
| Language | Go 1.24+ |
| MicroVM | Firecracker (primary), Cloud Hypervisor (alternative) |
| Database | PostgreSQL (sessions, agents, tasks) |
| Cache | Redis (session state, rate limiting) |
| Queue | NATS (async task processing) |
| Storage | S3-compatible, RustFS (agent workspaces, artifacts) |
| LLM | Gemini (Main), OpenAI, Anthropic, local models |
| Logging | slog (structured logging) |
| Metrics | Prometheus |
| Tracing | OpenTelemetry |

## Core Concepts

### Agent
An LLM-powered agent with identity, configuration, and capabilities. Lifecycle: **Created → Running → Suspended → Terminated**

### Session
Stateful interaction context between user and agent, bound to a microVM for execution. Maintains conversation history and execution context.

### MicroVM
Isolated execution environment with resource limits (CPU, memory, network, disk). Contains agent runtime, tools, and workspace.

### Task
Unit of work requested from an agent (code execution, file operations, API calls, etc.) executed within microVM for security.

### Tool
Capabilities available to agents (filesystem, network, code execution) with permission-based access control and sandboxed execution.

## Getting Started

### Prerequisites

- Go 1.24+
- PostgreSQL 14+
- Redis 7+
- Firecracker v1.5+ (or alternative VM backend)
- NATS Server 2.10+

### Installation

```bash
# Clone the repository
git clone https://github.com/yourorg/orchestrator.git
cd orchestrator

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

### Development Setup

```bash
# Run development environment setup
./scripts/dev-environment.sh

# Setup Firecracker (Linux)
sudo ./scripts/setup-firecracker.sh

# Build VM image
./scripts/build-vm-image.sh
```

### Configuration

Key configuration options in `configs/config.yaml`:

```yaml
server:
  http_port: 8080
  grpc_port: 9090

vm:
  provider: firecracker
  pool_size: 10
  max_vms: 100
  cpu_count: 2
  memory_mb: 512
  disk_mb: 2048
  network_enabled: false
  idle_timeout: 5m

llm:
  default_provider: gemini
  providers:
    gemini:
      api_key: ${GEMINI_API_KEY}
      model: gemini-2.5-pro
    openai:
      api_key: ${OPENAI_API_KEY}
      model: gpt-4
```

## API Usage

### Create a Session

```bash
curl -X POST http://localhost:8080/api/v1/sessions \
  -H "Content-Type: application/json" \
  -d '{
    "agent_id": "agent-123",
    "tools": ["filesystem", "code_execution"]
  }'
```

### Stream Messages (WebSocket)

```javascript
const ws = new WebSocket('ws://localhost:8080/api/v1/sessions/{id}/stream');

ws.onmessage = (event) => {
  const message = JSON.parse(event.data);
  console.log(message.data.content);
};

ws.send(JSON.stringify({
  type: 'user_message',
  content: 'Write a Python script to analyze this data'
}));
```

### Submit a Task

```bash
curl -X POST http://localhost:8080/api/v1/sessions/{id}/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "type": "code_execution",
    "language": "python",
    "code": "print(\"Hello from VM!\")"
  }'
```

## Development Guidelines

- Core domain (`src/core/domain/`) must remain infrastructure-agnostic
- Define interfaces in `src/core/ports/`, implement in `src/infrastructure/`
- Use repository pattern for all data access
- Implement all services in `src/service/` layer
- Keep `pkg/` for reusable utilities with no internal dependencies

## Security Model

1. User authentication (JWT/OAuth)
2. Agent permission validation
3. Tool permission check
4. Resource limit enforcement (CPU, RAM, disk, network)
5. VM network isolation (no internet by default)
6. Filesystem sandboxing (chroot/namespaces)
7. Syscall filtering (seccomp)
8. Execution timeout enforcement
9. Output size limits
10. Audit logging

## Observability

### Logging
Structured logging with slog. Request-scoped logger available via `pkglog.FromContext(ctx)`.

### Metrics
Prometheus metrics exposed on `/metrics`. Includes VM pool stats, task execution times, and API metrics.

### Tracing
OpenTelemetry tracing support. Configure endpoint in `config.yaml`.

## Roadmap

### Phase 1: Foundation (Current)
- [x] Basic project structure
- [ ] Core domain models (Agent, Session, Task, VM)
- [ ] Repository interfaces and Postgres implementation
- [ ] Basic HTTP API with health check

### Phase 2: VM Integration
- [ ] Firecracker provider implementation
- [ ] VM pool manager
- [ ] VM lifecycle management
- [ ] Resource monitoring

### Phase 3: LLM Integration
- [ ] LLM gateway with Gemini client
- [ ] Tool definition and execution framework
- [ ] Agent orchestration logic
- [ ] Session management with streaming

### Phase 4: Security & Production
- [ ] Authentication & authorization
- [ ] Resource limits & isolation
- [ ] Metrics & monitoring
- [ ] Load testing & optimization
- [ ] Documentation & deployment configs

## Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## License

MIT License - see LICENSE file for details

## Acknowledgments

- [Firecracker](https://firecracker-microvm.github.io/) - Secure and fast microVMs
- [NATS](https://nats.io/) - Cloud-native messaging system
- [OpenTelemetry](https://opentelemetry.io/) - Observability framework

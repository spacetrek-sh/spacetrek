# Architecture Design: LLM Orchestrator with MicroVM Integration

## Overview

This system orchestrates LLM agents with isolated microVM environments for secure, stateful execution.

## Design Principles

1. **Clean Architecture** - Separation of concerns with dependency inversion
2. **Security First** - VM isolation, resource limits, execution sandboxing
3. **Scalability** - Horizontal scaling of VM pools and orchestration layer
4. **Observability** - Comprehensive logging, metrics, and tracing
5. **Extensibility** - Plugin architecture for LLM providers and VM backends

---

## Core Domain Concepts

### 1. **Agent**

- Represents an LLM-powered agent
- Has identity, configuration, and capabilities
- Lifecycle: Created → Running → Suspended → Terminated

### 2. **Session**

- Stateful interaction context between user and agent
- Maintains conversation history and context
- Bound to a microVM instance for execution

### 3. **MicroVM**

- Isolated execution environment (Firecracker, Cloud Hypervisor, etc.)
- Contains agent runtime, tools, and workspace
- Resource-limited (CPU, memory, network, disk)

### 4. **Task**

- Unit of work requested from an agent
- Can be code execution, file operations, API calls, etc.
- Executed within microVM for security

### 5. **Tool**

- Capabilities available to agents (filesystem, network, code execution)
- Permission-based access control
- Sandboxed execution within VM

---

## Architectural Layers

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
│                                                              │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐     │
│  │   Agent      │  │   Session    │  │   Executor   │     │
│  │  Orchestrator│  │   Manager    │  │   Service    │     │
│  └──────────────┘  └──────────────┘  └──────────────┘     │
│                                                              │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐     │
│  │   VM Pool    │  │   LLM        │  │   Security   │     │
│  │   Manager    │  │   Gateway    │  │   Policy     │     │
│  └──────────────┘  └──────────────┘  └──────────────┘     │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│                   Core Domain Layer                          │
│  (Business Logic, Domain Models, Interfaces)                │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│                Infrastructure Layer                          │
│                                                              │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐     │
│  │   MicroVM    │  │   LLM        │  │   Database   │     │
│  │   Provider   │  │   Client     │  │   Repository │     │
│  │ (Firecracker)│  │ (OpenAI, etc)│  │  (Postgres)  │     │
│  └──────────────┘  └──────────────┘  └──────────────┘     │
│                                                              │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐     │
│  │   Storage    │  │   Message    │  │   Metrics    │     │
│  │   Backend    │  │   Queue      │  │   Collector  │     │
│  │   (S3/Local) │  │   (NATS)     │  │ (Prometheus) │     │
│  └──────────────┘  └──────────────┘  └──────────────┘     │
└─────────────────────────────────────────────────────────────┘
```

---

## Project Structure

```
orchestrator/
├── cmd/
│   ├── main.go                    # Application entry point
│   ├── migrate/                   # Database migration tool
│   └── worker/                    # Background worker processes
│
├── pkg/                           # Reusable packages (no internal dependencies)
│   ├── exception/                 # Error handling utilities
│   ├── log/                       # Logging utilities
│   ├── http/                      # HTTP utilities
│   ├── json/                      # JSON utilities
│   └── validation/                # Validation helpers
│
├── src/
│   ├── core/                      # Domain layer (business logic)
│   │   ├── domain/
│   │   │   ├── agent/
│   │   │   │   ├── agent.go           # Agent entity
│   │   │   │   ├── capability.go      # Agent capabilities
│   │   │   │   └── repository.go      # Agent repository interface
│   │   │   ├── session/
│   │   │   │   ├── session.go         # Session entity
│   │   │   │   ├── context.go         # Execution context
│   │   │   │   └── repository.go      # Session repository interface
│   │   │   ├── vm/
│   │   │   │   ├── vm.go              # MicroVM entity
│   │   │   │   ├── config.go          # VM configuration
│   │   │   │   ├── state.go           # VM state machine
│   │   │   │   └── provider.go        # VM provider interface
│   │   │   ├── task/
│   │   │   │   ├── task.go            # Task entity
│   │   │   │   ├── result.go          # Task result
│   │   │   │   └── repository.go      # Task repository interface
│   │   │   └── tool/
│   │   │       ├── tool.go            # Tool definition
│   │   │       ├── permission.go      # Tool permissions
│   │   │       └── executor.go        # Tool executor interface
│   │   │
│   │   └── ports/                 # Interface definitions (hexagonal arch)
│   │       ├── llm.go             # LLM provider interface
│   │       ├── storage.go         # Storage interface
│   │       └── queue.go           # Message queue interface
│   │
│   ├── service/                   # Business logic / Use cases
│   │   ├── agent/
│   │   │   ├── orchestrator.go    # Agent lifecycle orchestration
│   │   │   ├── creator.go         # Agent creation service
│   │   │   └── manager.go         # Agent management
│   │   ├── session/
│   │   │   ├── manager.go         # Session lifecycle management
│   │   │   ├── message.go         # Message handling
│   │   │   └── state.go           # State persistence
│   │   ├── execution/
│   │   │   ├── executor.go        # Task execution service
│   │   │   ├── scheduler.go       # Task scheduling
│   │   │   └── sandbox.go         # Sandboxed execution
│   │   ├── vm/
│   │   │   ├── pool.go            # VM pool management
│   │   │   ├── provisioner.go     # VM provisioning
│   │   │   ├── lifecycle.go       # VM lifecycle management
│   │   │   └── monitor.go         # VM health monitoring
│   │   └── security/
│   │       ├── policy.go          # Security policy enforcement
│   │       ├── resource_limiter.go# Resource limits
│   │       └── isolation.go       # Isolation verification
│   │
│   ├── infrastructure/            # External integrations
│   │   ├── vm/
│   │   │   ├── firecracker/
│   │   │   │   ├── provider.go    # Firecracker implementation
│   │   │   │   ├── api_client.go  # Firecracker API client
│   │   │   │   └── snapshot.go    # Snapshot management
│   │   │   └── cloud_hypervisor/
│   │   │       └── provider.go    # Cloud Hypervisor alternative
│   │   ├── llm/
│   │   │   ├── openai/
│   │   │   │   └── client.go      # OpenAI integration
│   │   │   ├── anthropic/
│   │   │   │   └── client.go      # Anthropic integration
│   │   │   └── gateway.go         # LLM gateway (routing, fallback)
│   │   ├── storage/
│   │   │   ├── s3/
│   │   │   │   └── backend.go     # S3 storage backend
│   │   │   └── local/
│   │   │       └── backend.go     # Local filesystem backend
│   │   ├── queue/
│   │   │   ├── nats/
│   │   │   │   └── client.go      # NATS message queue
│   │   │   └── memory/
│   │   │       └── queue.go       # In-memory queue (dev)
│   │   └── metrics/
│   │       └── prometheus/
│   │           └── collector.go   # Prometheus metrics
│   │
│   ├── repository/                # Data persistence
│   │   ├── postgres/
│   │   │   ├── agent.go           # Agent repository impl
│   │   │   ├── session.go         # Session repository impl
│   │   │   ├── task.go            # Task repository impl
│   │   │   └── connection.go      # Database connection
│   │   └── memory/
│   │       └── agent.go           # In-memory repo (testing)
│   │
│   ├── api/                       # API handlers
│   │   ├── http/
│   │   │   ├── v1/
│   │   │   │   ├── agent/
│   │   │   │   │   ├── handler.go     # Agent CRUD handlers
│   │   │   │   │   ├── dto.go         # Data transfer objects
│   │   │   │   │   └── routes.go      # Route definitions
│   │   │   │   ├── session/
│   │   │   │   │   ├── handler.go     # Session handlers
│   │   │   │   │   ├── websocket.go   # WebSocket for streaming
│   │   │   │   │   └── routes.go
│   │   │   │   ├── task/
│   │   │   │   │   ├── handler.go
│   │   │   │   │   └── routes.go
│   │   │   │   └── admin/
│   │   │   │       ├── vm_handler.go  # VM management
│   │   │   │       ├── metrics.go     # Metrics endpoint
│   │   │   │       └── routes.go
│   │   │   └── server.go          # HTTP server setup
│   │   └── grpc/
│   │       ├── proto/             # Protocol buffer definitions
│   │       └── server.go          # gRPC server
│   │
│   └── middleware/                # HTTP middleware
│       ├── auth.go                # Authentication
│       ├── ratelimit.go           # Rate limiting
│       ├── logging.go             # Request logging
│       ├── recovery.go            # Panic recovery
│       └── cors.go                # CORS handling
│
├── migrations/                    # Database schema migrations
│   ├── 000001_init.up.sql
│   └── 000001_init.down.sql
│
├── configs/                       # Configuration files
│   ├── config.yaml                # Application config
│   ├── vm/
│   │   └── default.json           # Default VM configuration
│   └── tools/
│       └── registry.yaml          # Available tools registry
│
├── scripts/                       # Utility scripts
│   ├── setup-firecracker.sh       # Firecracker setup
│   ├── build-vm-image.sh          # VM image builder
│   └── dev-environment.sh         # Dev environment setup
│
├── tests/
│   ├── integration/
│   │   ├── agent_test.go
│   │   └── vm_test.go
│   └── e2e/
│       └── orchestrator_test.go
│
├── docs/                          # Documentation
│   ├── api/                       # API documentation
│   ├── architecture/              # Architecture diagrams
│   └── deployment/                # Deployment guides
│
├── deployments/                   # Deployment configurations
│   ├── docker/
│   │   └── Dockerfile
│   └── kubernetes/
│       ├── deployment.yaml
│       └── service.yaml
│
├── go.mod
├── go.sum
├── Makefile
├── CLAUDE.md
├── ARCHITECTURE.md                # This file
└── README.md
```

---

## Key Design Patterns

### 1. **Hexagonal Architecture (Ports & Adapters)**

- **Core Domain** is isolated from infrastructure
- **Ports** define interfaces (in `core/ports/`)
- **Adapters** implement interfaces (in `infrastructure/`)
- Easy to swap implementations (e.g., different VM providers)

### 2. **Repository Pattern**

- Abstract data access through repository interfaces
- Domain layer defines interfaces, infrastructure implements
- Supports multiple backends (Postgres, in-memory for tests)

### 3. **Factory Pattern**

- VM creation: `VMProvisioner` factory creates VMs with proper config
- Agent creation: `AgentCreator` factory with validation
- Tool registration: `ToolRegistry` factory for tool instantiation

### 4. **Strategy Pattern**

- **LLM Gateway**: Route to different LLM providers based on requirements
- **VM Provider**: Switch between Firecracker, Cloud Hypervisor, etc.
- **Storage Backend**: Choose S3, local, or other storage

### 5. **Observer Pattern**

- **VM State Changes**: Notify listeners when VM state transitions
- **Task Events**: Emit events for task start, progress, completion
- **Session Updates**: Notify clients of session state changes

### 6. **Command Pattern**

- **Task Execution**: Tasks encapsulate commands to execute
- **Tool Invocation**: Tools are commands with undo/redo capability
- **Agent Actions**: Discrete actions that can be queued and replayed

### 7. **Pool Pattern**

- **VM Pool**: Pre-warmed VMs for fast agent startup
- **Connection Pool**: Database connection pooling
- **Worker Pool**: Background task processing

---

## Core Workflows

### Agent Session Flow

```
1. User creates session → POST /api/v1/sessions
2. Orchestrator requests VM from pool
3. If pool empty, provision new VM
4. Session binds to VM
5. Agent loaded into VM with tools
6. User sends message → WebSocket /api/v1/sessions/{id}/stream
7. Message sent to LLM via gateway
8. LLM response may include tool calls
9. Tools executed in VM sandbox
10. Results sent back to LLM
11. Final response streamed to user
12. Session state persisted
```

### VM Lifecycle

```
1. Pool manager pre-provisions VMs
2. VM created with base image + runtime
3. VM enters Ready state in pool
4. Session request → VM assigned → Running state
5. Session activity → VM executes tasks
6. Session idle → VM enters Idle state
7. Idle timeout → VM suspended or terminated
8. Pool manager replenishes VMs
```

### Security Model

```
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
```

---

## Technology Stack Recommendations

### Core

- **Language**: Go 1.23+
- **Framework**: net/http or Gin/Echo for REST
- **Async**: Goroutines + Channels

### MicroVM

- **Primary**: Firecracker (AWS, production-grade)
- **Alternative**: Cloud Hypervisor (Intel/AMD optimization)
- **Container fallback**: gVisor (for Mac/Windows dev)

### Data Layer

- **Database**: PostgreSQL (sessions, agents, tasks)
- **Cache**: Redis (session state, rate limiting)
- **Queue**: NATS (async task processing)
- **Storage**: S3-compatible (agent workspaces, artifacts)

### LLM Integration

- **Providers**: OpenAI, Anthropic, local models
- **Protocol**: HTTP/REST, streaming SSE
- **Retry**: Exponential backoff with fallback

### Observability

- **Logging**: slog (structured logging)
- **Metrics**: Prometheus
- **Tracing**: OpenTelemetry
- **APM**: Jaeger or Grafana Tempo

### Security

- **VM Isolation**: Firecracker microVMs
- **Network**: iptables/nftables for VM networking
- **Secrets**: HashiCorp Vault or AWS Secrets Manager
- **Auth**: JWT tokens, OAuth2

---

## Configuration Management

### Environment-based Config

```yaml
# configs/config.yaml
environment: development

server:
  http_port: 8080
  grpc_port: 9090
  read_timeout: 30s
  write_timeout: 30s

database:
  host: localhost
  port: 5432
  name: orchestrator
  max_connections: 50

vm:
  provider: firecracker
  pool_size: 10
  max_vms: 100
  cpu_count: 2
  memory_mb: 512
  disk_mb: 2048
  network_enabled: false
  idle_timeout: 5m
  max_lifetime: 1h

llm:
  default_provider: openai
  timeout: 60s
  max_retries: 3
  providers:
    openai:
      api_key: ${OPENAI_API_KEY}
      model: gpt-4
    anthropic:
      api_key: ${ANTHROPIC_API_KEY}
      model: claude-3-sonnet

security:
  jwt_secret: ${JWT_SECRET}
  max_task_duration: 5m
  max_memory_per_task: 256MB
  max_disk_per_session: 1GB

observability:
  log_level: info
  metrics_enabled: true
  tracing_enabled: true
  tracing_endpoint: localhost:4318
```

---

## Next Steps

### Phase 1: Foundation (Week 1-2)

- [ ] Setup basic project structure
- [ ] Implement core domain models (Agent, Session, Task, VM)
- [ ] Create repository interfaces and Postgres implementation
- [ ] Basic HTTP API with health check

### Phase 2: VM Integration (Week 3-4)

- [ ] Firecracker provider implementation
- [ ] VM pool manager
- [ ] VM lifecycle management
- [ ] Resource monitoring

### Phase 3: LLM Integration (Week 5-6)

- [ ] LLM gateway with OpenAI client
- [ ] Tool definition and execution framework
- [ ] Agent orchestration logic
- [ ] Session management with streaming

### Phase 4: Security & Production (Week 7-8)

- [ ] Authentication & authorization
- [ ] Resource limits & isolation
- [ ] Metrics & monitoring
- [ ] Load testing & optimization
- [ ] Documentation & deployment configs

---

## Open Questions to Discuss

1. **VM Provider**: Start with Firecracker or use containers (gVisor) for easier dev?
2. **State Persistence**: How long should sessions be kept? Archive strategy?
3. **Tool Framework**: Pre-defined tools vs. user-defined tools?
4. **Multi-tenancy**: Single tenant or multi-tenant from day 1?
5. **Scaling**: Horizontal scaling approach (stateless orchestrators + VM pools)?
6. **Network Policy**: Allow internet access from VMs? If yes, with what restrictions?

# spacetrk

This project is an LLM Orchestrator Agent that mainly focuses on giving LLM Agent access to microVM for better security and stateful environment.

## Architecture Summary

### Design Principles
- **Clean Architecture** - Separation of concerns with dependency inversion (Hexagonal/Ports & Adapters)
- **Security First** - VM isolation, resource limits, execution sandboxing
- **Scalability** - Horizontal scaling of VM pools and orchestration layer
- **Observability** - Comprehensive logging, metrics, and tracing
- **Extensibility** - Plugin architecture for LLM providers and VM backends

### Core Domain Concepts
- **Agent**: LLM-powered agent with identity, configuration, and capabilities. Lifecycle: Created → Running → Suspended → Terminated
- **Session**: Stateful interaction context between user and agent, bound to a microVM for execution
- **MicroVM**: Isolated execution environment (Firecracker, Cloud Hypervisor) with resource limits
- **Task**: Unit of work executed within microVM for security
- **Tool**: Agent capabilities with permission-based access control

### Project Structure
```
orchestrator/
├── cmd/                    # Application entry point, migrations, workers
├── pkg/                    # Reusable packages (no internal dependencies)
│   ├── exception/          # Domain errors: AppError, FieldError, code constructors
│   │   ├── exception.go    # AppError type, New/Wrap/FromError
│   │   └── codes.go        # FieldError, error code constants, helpers (NotFound, BadRequest…)
│   ├── http/               # HTTP utilities (package httputil)
│   │   ├── response.go     # WriteJSON / WriteError / Created / NoContent
│   │   └── request.go      # DecodeJSON / BindJSON / ParseMultipart / FormFile
│   ├── json/               # JSON helpers (package jsonutil) — Marshal/Unmarshal with AppErrors
│   ├── grpc/               # gRPC utilities (package grpcutil)
│   │   └── status.go       # AppError ↔ gRPC status conversion
│   ├── log/                # Structured logger
│   │   ├── logger.go       # New() — tint (dev) / JSON (prod) slog handler
│   │   └── context.go      # WithLogger / FromContext — context-scoped logger
│   └── validation/         # Struct validation (package validation)
│       └── validator.go    # Struct() / Var() using go-playground/validator with json tag names
├── src/
│   ├── core/               # Domain layer (business logic, domain models, interfaces)
│   │   ├── domain/         # Entities (agent, session, vm, task, tool)
│   │   └── ports/          # Interface definitions (hexagonal arch)
│   ├── service/            # Business logic / Use cases
│   ├── infrastructure/     # External integrations (vm, llm, storage, queue)
│   ├── repository/         # Data persistence (postgres, in-memory)
│   ├── api/                # HTTP/gRPC handlers
│   └── middleware/         # HTTP middleware (auth, ratelimit, logging)
│       ├── requestid.go    # RequestID middleware + GetRequestID(ctx)
│       ├── logging.go      # Logging middleware — attaches request-scoped logger to ctx
│       ├── recovery.go     # Recovery middleware — catches panics → 500 JSON
│       └── grpc/
│           └── interceptors.go  # UnaryLogging/Recovery/Validation + StreamLogging/Recovery + ServerOptions()
├── migrations/             # Database schema migrations
├── configs/                # Configuration files
├── scripts/                # Utility scripts
└── tests/                  # Integration and E2E tests
```

### Key Design Patterns
- **Hexagonal Architecture (Ports & Adapters)**: Core domain isolated from infrastructure
- **Repository Pattern**: Abstract data access through interfaces
- **Factory Pattern**: VM/Agent/Tool creation with proper configuration
- **Strategy Pattern**: LLM Gateway, VM Provider, Storage Backend selection
- **Observer Pattern**: VM state changes, task events, session updates
- **Command Pattern**: Task execution, tool invocation, agent actions
- **Pool Pattern**: VM pool, connection pool, worker pool

### Technology Stack
- **Language**: Go 1.24+
- **MicroVM**: Firecracker (primary)
- **Database**: PostgreSQL (sessions, agents, tasks), Redis (cache, rate limiting)
- **Queue**: NATS (async task processing)
- **Storage**: S3-compatible, RustFS (recommended) (agent workspaces, artifacts)
- **LLM**: Gemini (Main), OpenAI, Anthropic, local models via gateway
- **Observability**: slog, Prometheus, OpenTelemetry

### API Foundation Conventions
- All HTTP responses use `{"data": ..., "error": ...}` envelope (see `pkg/http/response.go`)
- Domain errors always use `*exception.AppError` — use constructors in `pkg/exception/codes.go`
- Validation errors carry `[]FieldError` details; use `validation.Struct()` for struct validation
- Context logger: call `pkglog.FromContext(ctx)` in handlers — the Logging middleware pre-populates it
- Standard HTTP middleware chain: `RequestID → Logging → Recovery → (auth) → handler`
- gRPC server: use `grpcmiddleware.ServerOptions(logger)` to wire all interceptors at once

### Key Workflows
1. **Agent Session**: User creates session → VM assigned from pool → Agent loaded → User messages via WebSocket → LLM processes → Tools executed in VM → Results streamed back
2. **VM Lifecycle**: Pool pre-provisions VMs → Ready state → Assigned (Running) → Idle → Suspended/Terminated → Pool replenishes
3. **Security Model**: Auth → Permission validation → Resource limits → VM isolation → Sandbox → Syscall filtering → Timeout enforcement → Audit logging

### Development Guidelines
- Core domain (`src/core/domain/`) must remain infrastructure-agnostic
- Define interfaces in `src/core/ports/`, implement in `src/infrastructure/`
- Use repository pattern for all data access
- Implement all services in `src/service/` layer
- Keep `pkg/` for reusable utilities with no internal dependencies

# spacetrk

LLM Orchestrator Agent with microVM isolation for secure, stateful code execution.

## Architecture Summary

### Design Principles
- **Clean Architecture** - Hexagonal/Ports & Adapters with dependency inversion
- **Security First** - VM isolation, resource limits, execution sandboxing
- **Scalability** - Horizontal scaling of VM pools and orchestration layer
- **Extensibility** - Plugin architecture for LLM providers and VM backends

### Core Domain Concepts
- **User**: Authenticated user with role-based access (admin/user). JWT access + refresh tokens.
- **Agent**: LLM-powered agent with identity, configuration, and capabilities. Lifecycle: Created → Running → Suspended → Terminated
- **Chat**: Conversation context between user and agent, bound to a microVM. Replaces "Session" concept.
- **MicroVM**: Isolated execution environment (Firecracker) with CPU, memory, disk, and network limits. Connected via vsock.
- **Task**: Unit of work tracked through orchestration (LLM inference, tool call, code execution).
- **Tool**: Agent capabilities exposed to the orchestrator's ReAct loop (vm.create, vm.start, vm.list, vm.execute_command, vm.stop, vm.snapshot).
- **Environment**: Pre-configured VM template (currently seeded: `ubuntu`, `bun`, `uv`) with image path and resource defaults. UUIDs derived deterministically from the seed namespace UUID via `uuid.NewSHA1(ns, "environment:"+type)`.
- **Snapshot**: VM state persistence to S3/RustFS for restore across chat sessions.
- **Volume**: Workspace mounts (local directory, GitHub repo, S3).

### Project Structure
```
orchestrator/
├── cmd/
│   ├── main.go             # Application entry point
│   └── seed/               # Database seeder
├── pkg/                    # Reusable packages (no internal dependencies)
│   ├── auth/jwt/           # JWT token generation and validation
│   ├── config/             # Configuration loading
│   ├── exception/          # AppError, FieldError, error code constructors
│   ├── http/               # WriteJSON/WriteError/Created/NoContent, BindJSON/DecodeJSON
│   ├── json/               # Marshal/Unmarshal with AppError support
│   ├── log/                # Structured logger (tint dev / JSON prod slog handler)
│   └── validation/         # Struct validation via go-playground/validator
├── src/
│   ├── core/
│   │   ├── domain/         # Entities: agent, auth, chat, environment, orchestrator, snapshot, tool, user, vm, volume
│   │   └── ports/          # Interfaces: LLM, ToolRegistry, StateStore, SnapshotStore
│   ├── service/
│   │   ├── agent/          # Agent CRUD service
│   │   ├── auth/           # Authentication (token pair generation, refresh, revocation)
│   │   ├── chat/           # Chat service with async orchestration, VM resolver, SSE streaming
│   │   ├── orchestrator/   # ReAct loop runtime, tool planning, event streaming, state store
│   │   ├── tool/           # VM tools: vm_create, vm_start, vm_list, vm_command, vm_stop, vm_snapshot
│   │   ├── user/           # User registration, profile, password management
│   │   └── vm/             # VM lifecycle, vsock exec, snapshot/restore, metrics, lease tracking, hostswriter (dnsmasq addn-hosts)
│   ├── infrastructure/
│   │   ├── llm/gemini/     # Gemini API client with tool calling, streaming, planner
│   │   ├── storage/s3/     # S3-compatible snapshot storage (RustFS)
│   │   └── vm/firecracker/ # Firecracker provider: lifecycle, vsock protocol, networking, CID allocation
│   ├── repository/
│   │   ├── postgres/       # PostgreSQL repos: agent, auth, chat, environment, user, vm, vm_metrics_history, runtime_event, snapshot
│   │   └── memory/         # In-memory fallback repos: agent, chat, environment, vm, runtime_event
│   ├── api/http/
│   │   ├── server.go       # chi router, middleware chain, route registration
│   │   └── v1/             # Handlers by domain: auth/, agent/, chat/, vm/
│   └── middleware/
│       ├── auth.go         # JWT authentication + RequireRole middleware
│       ├── correlationid.go # Correlation ID propagation
│       ├── requestid.go    # RequestID middleware + GetRequestID(ctx)
│       ├── logging.go      # Attaches request-scoped logger to ctx
│       ├── recovery.go     # Panic recovery → 500 JSON
│       └── grpc/           # Unary/Stream logging, recovery, validation interceptors
├── migrations/             # 35 PostgreSQL migrations (enums → all tables → runtime events → VM naming)
├── configs/
│   ├── config.yaml         # Active configuration
│   └── config.yaml.example # Template with environment variables
├── scripts/
│   └── entrypoint.sh       # Docker entrypoint
├── docs/                   # Architecture docs, ERD, OpenAPI spec, design docs
└── tests/                  # Integration and E2E tests
```

### Key Design Patterns
- **Hexagonal Architecture (Ports & Adapters)**: Core domain isolated from infrastructure
- **Repository Pattern**: Abstract data access through interfaces (postgres + memory implementations)
- **Strategy Pattern**: LLM Gateway, VM Provider, Storage Backend selection
- **Observer Pattern**: Runtime events streamed via SSE for real-time client updates
- **ReAct Loop**: LLM iteratively selects tools until final answer or step limit (max 10)
- **Dual-Consumer Streaming**: LLM path gets buffered tool results; client path gets real-time SSE events (thinking, answer, tool_start, tool_end)
- **VM Lease Tracking**: Assignment records for chat→VM binding with auto-resume across sessions
- **CID Allocation**: Collision-detecting vsock CID allocator for guest agent communication
- **Lifecycle Hooks**: `vm.Service.SetLifecycleHook` notifies observers after VM Create/Destroy. Used by `hostswriter.Hook` (rewrites dnsmasq `addn-hosts`); future tunnel/proxy writers reuse the same pattern.

### Networking Architecture
- **Topology**: One TAP device per VM on the host. No bridge (`EnsureBridge` is a no-op in `src/infrastructure/vm/firecracker/network.go`). The bridge was deliberately skipped to avoid `br_netfilter` conflicts with Docker.
- **Subnet**: `10.200.0.0/16` (config: `vm.firecracker.network.subnet`). IPs allocated sequentially by `IPAllocator` (`src/service/vm/service.go`) across `10.200.0.2` → `10.200.255.254`. Theoretical max ~65,533 VMs; vsock CID pool (`cid_min` 1024 → `cid_max` 65535) caps it at ~64,512.
- **Gateway**: `10.200.0.1` assigned to each TAP as a `/32` point-to-point address. Each VM also gets a host-side `/32` route (`ip route replace <vmIP>/32 dev tap-<vmID>`).
- **Guest config**: Kernel cmdline `ip=<vmIP>::10.200.0.1:255.255.0.0::eth0:off` — guests believe they are on a `/16`.
- **Proxy ARP**: Enabled on every TAP (`proxy_arp=1`) so the host answers ARP on behalf of any routable VM IP. Combined with `ip_forward=1` and `policy accept` on the forward chain, **VMs can reach each other by IP** — VM-A → tap-A → host route lookup → tap-B → VM-B.
- **Outbound NAT**: nft MASQUERADE on `table ip spacetrk-nat` for `10.200.0.0/16 → <outbound>`. Internet → VM inbound is not exposed (no DNAT rules).
- **DNS**: dnsmasq runs **inside the `spacetrek-api` container** (`scripts/entrypoint.sh`), bound to `lo`, `br-stk`, and any `tap*` interface via `bind-dynamic` — **not** the docker bridge IP. Caching forwarder to `1.1.1.1` / `8.8.8.8` for upstream queries, plus a local `vm.internal` zone populated from `/var/lib/spacetrek/vm-hosts` (rendered by `hostswriter`). Guests resolve via `10.200.0.1:53`; **VM-name resolution works end to end inside the mesh** (see `docs/issues/vm-dns-naming.md`, status: Resolved). The host OS cannot query this dnsmasq without a route into `10.200.0.0/16` — see Deployment Topology.
- **CID allocation**: `src/infrastructure/vm/firecracker/cid_allocator.go` allocates vsock Context IDs with collision detection.

### Deployment Topology

`docker compose up` brings up 4 containers on the `spacetrek_default` bridge (`172.19.0.0/16`):

| Container | Image | Purpose |
|---|---|---|
| `spacetrek-api` | `spacetrek-api` | Orchestrator (HTTP API, Firecracker, LLM client, dnsmasq). **Privileged**; mounts `/dev/kvm`, `/dev/net/tun`, `/dev/mapper/control`, `/dev/loop-control`. Port 8080 published. |
| `spacetrek-psql` | `postgres:17-alpine` | PostgreSQL. Port 5432 published. |
| `spacetrek-rustfs` | `rustfs/rustfs:latest` | S3-compatible snapshot/object storage. Ports 9000-9001 published. |
| `spacetrek-interface` | `spacetrek-interface` | Frontend (Vite dev server). Port 5173 published. |

**The entire VM mesh — `10.200.0.0/16`, TAPs, dnsmasq, Firecracker processes — lives in `spacetrek-api`'s network namespace.** The host OS has no route to `10.200.x.y` by default: pinging `10.200.0.2` from the host fails (`From 10.11.12.1 Time to live exceeded`). Container-to-VM, VM-to-VM, and VM-to-internet all work; host-to-VM requires `ip route add 10.200.0.0/16 via <api-container-ip>` (tracked in `docs/issues/host-vm-exposure.md`).

Public traffic for `*.spacetrek.xyz` is terminated by **Cloudflare Tunnel**: `cloudflared` runs on the host as a systemd service, establishes outbound tunnels to Cloudflare's edge, and routes ingress to local services. No inbound public ports are required for the production path.

### Technology Stack
- **Language**: Go 1.24+
- **MicroVM**: Firecracker with vsock guest agent
- **Database**: PostgreSQL (35 migrations covering full schema)
- **Cache**: Redis (planned for session state, rate limiting)
- **Storage**: S3-compatible / RustFS (VM snapshots, agent artifacts)
- **LLM**: Gemini (primary), extensible gateway for OpenAI/Anthropic
- **Router**: chi with CORS support
- **Auth**: JWT (access + refresh tokens, bcrypt password hashing)
- **Observability**: slog, Prometheus, OpenTelemetry

### API Routes

All routes are under `/api/v1` (except `/health`).

| Group | Method | Path | Auth | Description |
|-------|--------|------|------|-------------|
| Health | GET | `/health` | No | Health check |
| Auth | POST | `/auth/register` | No | Register user |
| Auth | POST | `/auth/login` | No | Login, returns token pair |
| Auth | POST | `/auth/refresh` | No | Refresh access token |
| Auth | POST | `/auth/logout` | Yes | Revoke all refresh tokens |
| Auth | GET | `/auth/me` | Yes | Get current user |
| Auth | PUT | `/auth/profile` | Yes | Update profile |
| Auth | PUT | `/auth/password` | Yes | Change password |
| Agent | POST | `/agents` | Yes | Create agent |
| Agent | GET | `/agents` | Yes | List agents (paginated) |
| Agent | GET | `/agents/{id}` | Yes | Get agent |
| Agent | PUT | `/agents/{id}` | Yes | Update agent |
| Agent | DELETE | `/agents/{id}` | Yes | Delete agent |
| Chat | POST | `/chat` | Yes | Send message (async, returns 202) |
| Chat | GET | `/chat` | Yes | List conversations (cursor-paginated) |
| Chat | GET | `/chat/{id}` | Yes | Get chat |
| Chat | GET | `/chat/{id}/messages` | Yes | List messages (cursor-paginated) |
| Chat | GET | `/chat/{id}/stream` | Yes | SSE runtime event stream |
| Chat | DELETE | `/chat/{id}` | Yes | Close chat |
| VM | POST | `/vm` | Admin | Create VM |
| VM | GET | `/vm/leases` | Admin | List active leases by chat |
| VM | GET | `/vm/runtimes` | Admin | List running VMs with metrics |
| VM | GET | `/vm/runtimes/stream` | Admin | SSE stream of all runtimes |
| VM | GET | `/vm/fleet/stream` | Yes | SSE fleet metrics (frontend-shaped, user-scoped) |
| VM | GET | `/vm/activity/stream` | Yes | SSE global activity event feed (user-scoped) |
| VM | GET | `/vm/{id}` | Admin | Get VM details |
| VM | DELETE | `/vm/{id}` | Admin | Stop VM |
| VM | DELETE | `/vm/{id}/destroy` | Admin | Destroy VM |
| VM | POST | `/vm/{id}/assign` | Admin | Assign VM to chat |
| VM | POST | `/vm/{id}/unassign` | Admin | Unassign VM |
| VM | POST | `/vm/{id}/execute` | Admin | Execute command via vsock |
| VM | POST | `/vm/{id}/snapshot` | Admin | Create VM snapshot |
| VM | GET | `/vm/{id}/metrics` | Admin | Current VM metrics |
| VM | GET | `/vm/{id}/metrics/history` | Admin | Historical metrics |
| VM | GET | `/vm/{id}/stream` | Admin | SSE runtime snapshot stream |
| VM | POST | `/vm/resume` | Admin | Resume VM from previous lease |

### API Foundation Conventions
- All HTTP responses use `{"data": ..., "error": ...}` envelope (see `pkg/http/response.go`)
- Domain errors always use `*exception.AppError` — use constructors in `pkg/exception/codes.go`
- Validation errors carry `[]FieldError` details; use `validation.Struct()` for struct validation
- Context logger: call `pkglog.FromContext(ctx)` in handlers — the Logging middleware pre-populates it
- Standard HTTP middleware chain: `CorrelationID → RequestID → Logging → Recovery → (auth) → handler`
- CORS enabled for `http://localhost:5173` (frontend dev server)
- Chat endpoints return 202 Accepted for async processing; use SSE `/stream` for real-time updates
- Pagination: offset/limit for agents, cursor-based for chats and messages
- gRPC server: use `grpcmiddleware.ServerOptions(logger)` to wire all interceptors at once
- `POST /api/v1/vm` requires `environment_id` (UUID) and `conversation_id` in the JSON body — **not** the environment type string. Unknown JSON fields are rejected by a strict decoder. Environment UUIDs are derived deterministically from the seed namespace UUID (`a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d`) via `uuid.NewSHA1(ns, "environment:"+type)`; current seeded types: `ubuntu`, `bun`, `uv` (see `seeds/environments.json`).

### Key Workflows

1. **Chat Orchestration (ReAct Loop)**:
   User sends message → Chat service resolves/creates VM (via VM resolver) → Orchestrator runs ReAct loop → LLM generates response or selects tools → Tools execute in VM via vsock → Results fed back to LLM → Final answer streamed via SSE events (thinking, answer, tool_start, tool_end) → Runtime events persisted to database

2. **VM Lifecycle**:
   Pool pre-provisions VMs → Ready state → Assigned to chat via lease → Running → Commands executed via vsock guest agent → Metrics sampled periodically → Idle timeout → Suspended/Terminated → Pool replenishes

3. **VM Resume/Auto-Assign**:
   Chat message arrives → VM resolver checks for existing lease → If found and VM stopped: snapshot restore + resume → If no lease: create new VM → VM assigned via lease record

4. **Security Model**: JWT auth → Role check (admin/user) → Permission validation → Resource limits → VM isolation → vsock sandbox → Execution timeout → Output size limits → Audit logging

### Development Guidelines
- Core domain (`src/core/domain/`) must remain infrastructure-agnostic
- Define interfaces in `src/core/ports/`, implement in `src/infrastructure/`
- Use repository pattern for all data access (postgres + memory implementations)
- Implement all services in `src/service/` layer
- Keep `pkg/` for reusable utilities with no internal dependencies
- Keep runtime orchestration logic in service layer; handlers should stay thin and delegate
- Treat tool contracts as domain/port boundaries, not infrastructure-specific types
- All new database schema changes require paired up/down migrations in `migrations/`
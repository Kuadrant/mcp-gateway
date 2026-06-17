# CLAUDE.md

## Project Overview

MCP Gateway: an Envoy-based gateway for Model Context Protocol (MCP) servers.

Two binaries, four components:

**`cmd/mcp-broker-router/main.go`** — data-plane:
- **Router**: Envoy ext_proc, routes MCP requests (gRPC :50051)
- **Broker**: aggregates tools from upstream MCP servers (HTTP :8080/mcp)

**`cmd/main.go`** — control-plane:
- **Controller**: discovers MCP servers via MCPServerRegistration and MCPVirtualServer CRDs
- **Operator**: reconciles MCPGatewayExtension, deploys router + broker instances

## Architecture

```
Client → Gateway (Envoy) → Router (ext_proc) → Broker → Upstream MCP Servers
                ↑                                 ↑
           Controller → Secret ──────────────────┘
```

- Controller watches CRDs, discovers backends via HTTPRoutes, writes config Secret
- Broker reads config Secret, connects to upstreams, federates tools with prefixes
- Router parses MCP requests, adds auth headers, tells Envoy where to route
- Tool calls use lazy init: router hairpins an initialize request back through Envoy before the tool/call
- All MCP traffic flows through Envoy for consistent policies

Istio is ONLY a Gateway API provider — no sidecars, no ambient mode, no service mesh. Just istiod programming the Gateway's Envoy proxy.

## Authentication (Two Separate Paths)

1. **Broker → upstream** (`credentialRef`): broker uses MCPServerRegistration's `credentialRef` for tool listing and session management. NEVER injected into client tool/call requests. Router has no access to `credentialRef`.

2. **Client → upstream** (`tools/call`): routed directly to backend via Envoy. Clients authenticate via AuthPolicy (OIDC, API key), URL token elicitation, or client-provided headers.

Different AuthPolicies can apply per MCP server (each has its own HTTPRoute).

## Code Style

- Minimal, DRY, terse comments (lowercase, only when necessary)
- Idiomatic Go, leverage interfaces where appropriate
- No emojis or AI-style formatting
- Files must end with newline
- Run `make lint` regularly

## Concurrency

Channels over mutexes. Share memory by communicating, not the other way around.

## Performance

Broker and router are hot paths. Avoid allocations in per-request code.

- Pointer maps (`map[string]*T`) not value maps
- `for i := range` not `for _, v := range` on large structs
- Structured logging (`logger.Info("msg", "key", val)`) not `fmt.Sprintf`
- `logger.Debug` for per-request, `logger.Info` for lifecycle only
- Injected `logger`, never package-level `slog.Info`/`slog.Error`
- Don't construct expensive args for span attributes on hot paths

See `docs/design/performance.md` for rationale.

## Running Tests

```bash
make lint                         # lint and style checks
make test-unit                    # unit tests
make test-controller-integration  # controller integration tests (envtest)
```

## Design Docs (read before modifying related areas)

- `docs/design/overview.md` — system architecture and component responsibilities
- `docs/design/routing.md` — request routing and ext_proc flow
- `docs/design/session-mgmt.md` — session lifecycle and JWT handling
- `docs/design/performance.md` — hot path constraints and rationale
- `docs/design/security-architecture.md` — auth model and threat boundaries

## Exploration

Use codebase-memory-mcp to index and explore the project when available.

## Contributing

Read `CONTRIBUTING.md` before creating PRs or issues. Feature, planned or speculative work requires an existing issue; bugfixes and security patches are exempt.

## Reference

- CRD docs: `docs/reference/`
- User guides: `docs/guides/` (published at docs.kuadrant.io)
- Doc guidelines: `docs/CLAUDE.md`

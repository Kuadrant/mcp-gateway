# CLAUDE.md

This file provides guidance to Claude Code when working with this repository.

## Project Overview

MCP Gateway is an Envoy-based gateway for Model Context Protocol (MCP) servers. Single binary (`mcp-broker-router`) with three components:
- **MCP Router**: Envoy external processor that routes MCP requests (gRPC on :50051)
- **MCP Broker**: HTTP service that aggregates tools from multiple MCP servers (HTTP on :8080/mcp)
- **MCP Gateway Controller**: Kubernetes controller that discovers MCP servers via MCPServerRegistration CRDs
- **MCP Gateway Operator**: Kubernetes controller that reconciles MCPGatewayExtension resources and implements the operator pattern to deploy instances of the MCP Router and MCP Broker to form a working MCP Gateway instance.

### Not Implemented
- Resource/prompt federation (only tools currently)

# Exploration

To explore the code base, if the codebase-memory-mcp is configured, index the project and use this MCP server and use its tools to explore the project as much as possible.

## Architecture

```
Client → Gateway (Envoy) → Router (ext_proc) → Broker → Upstream MCP Servers
                ↑                                 ↑
           Controller → ConfigMap ────────────────┘
```

- Controller watches MCPServerRegistration CRDs, discovers backends via HTTPRoutes, writes ConfigMap
- Broker reads ConfigMap, connects to upstream servers, federates tools with prefixes
- Router parses MCP requests, adds auth headers, tells Envoy where to route
- Tool Calls use a lazy initialization model where the router hairpins an initialize request back through Envoy to the backend MCP Server before continuing with the MCP tool call. This ensures initialization is only done when needed and that policies are applied to both the initialize and tool/call requests.
- All MCP traffic flows through Envoy for consistent policies

- Overview Documentation: `docs/design/overview.md`

### Core Components

- `Router`: ext_proc server that parses MCP Requests and routes the request to the correct MCP Backend
- `Broker`: default MCP Backend for the Gateway. It handles initialize and tool/list requests. Manages session initialization for the MCP Gateway. Serves aggregated tools for the gateway and handles backend MCP Server discovery and integration.
- `controller`: Kubernetes based controller that manages the CRDs defined by the code in `api/v1alpha1`
- `operator`: Kubernetes based controller that is responsible for deploying instances of the router and broker which together form the MCP Gateway

**Important**: We use Istio ONLY as a Gateway API provider, NOT as a service mesh:
- No sidecars on any workload pods
- No ambient mode (no ztunnels or waypoint proxies)
- Just `istiod` programming the Gateway's Envoy proxy
- ServiceEntry/DestinationRule only used for external service routing

### External Services

The controller detects external services by checking the HTTPRoute backendRef kind:
- `kind: Hostname` with `group: networking.istio.io` → treated as an external service, URL built directly from the hostname
- `kind: Service` (default) → treated as an internal Kubernetes service, URL built from `{name}.{namespace}.svc.cluster.local`

Users must create the Istio ServiceEntry, DestinationRule, and HTTPRoute resources for external services. See `docs/guides/external-mcp-server.md` for detailed instructions.

### Authentication

Broker authenticates with MCP Servers fronted by the Gateway via a credential in the config secret. This credential is stored in a secret referenced by the MCPServerRegistration resource. This credential is NOT and MUST NOT be used for client requests from outside the gateway.

Users authenticate based AuthPolicies applied on the Gateway Resource or the HTTPRoute resource. There can be different policies for each MCP Server as each server has its own HTTPRoute. There can be two forms of Auth applied 1) The policy applied to access the Gateway route and the MCP Broker endpoints 2) A distinct policy applied to a listener or HTTPRoute for a specific MCP server for example leveraging a PAT for GH MCP access.



## Key Dependencies

- `github.com/mark3labs/mcp-go` — MCP server/client SDK (JSON-RPC 2.0, SSE transport)
- `sigs.k8s.io/controller-runtime` — Kubebuilder controller framework
- `sigs.k8s.io/gateway-api` — Gateway API types (HTTPRoute, Gateway)
- `istio.io/client-go` — Istio EnvoyFilter types
- `github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3` — Envoy ext_proc protocol
- `github.com/golang-jwt/jwt/v5` — JWT session tokens
- `github.com/onsi/ginkgo/v2` + `github.com/onsi/gomega` — BDD test framework
- `go.opentelemetry.io/otel` — OpenTelemetry tracing/metrics/logs

## Key Files

- `cmd/mcp-broker-router/main.go`: Binary entry point for MCP Gateway both the broker and router components
- `cmd/main.go`: Binary entry point for controller and operator
- `internal/broker/broker.go`: MCP broker implementation
- `internal/broker/upstream/manager.go`: Backend MCP manager. Connects to MCP Servers, ensures they are alive, aggregates tools
- `internal/mcp-router/server.go`: Envoy external processor. Key file for the router
- `internal/mcp-router/request_handlers.go` request handling and routing logic for the router component
- `internal/mcp-router/response_handlers.go` response handling logic for the router component
- `internal/controller/mcpserverregistration_controller.go`: MCPServerRegistration reconciliation. Controller component
- `internal/controller/mcpgatewayextension_controller.go`: MCPGatewayExtension reconciliation. Operator component
- `internal/controller/mcpvirtualserver_controller.go`: MCPVirtualServer reconciliation. Controller Component
- `internal/config/config_writer.go`: Configuration management used by the controller to manage the configuration for the broker and router components
- `internal/session/cache.go`: cache integration for the router and broker components. It stores session information.
- `internal/session/jwt.go`: JWT based session manager.
- `internal/clients/`: MCP client for the internal hairpinned initialize during a tool/call
- `internal/idmap/`: Maps gateway-assigned request IDs to backend server request IDs
- `internal/otel/`: OpenTelemetry instrumentation (tracing, metrics, logs)
- `internal/tests/`: Shared test utilities

## Development

### Code Style

- Minimal, DRY, terse comments (lowercase, only when necessary)
- Idiomatic Go, leverage interfaces where appropriate use Go specific plugins, language servers and agents if available
- No emojis or AI-style formatting
- Files must end with newline
- Regularly run make lint to check for lint errors.
- When adding or changing CRD fields in `api/v1alpha1/`, update the corresponding API reference doc in `docs/reference/` to reflect the change.

### Development Checklists

#### Adding a new upstream feature
- [ ] Update `MCPManager` in `internal/broker/upstream/manager.go`
- [ ] Add corresponding broker handling in `internal/broker/broker.go`
- [ ] Update config types in `internal/config/types.go` if new config fields needed
- [ ] Add unit tests alongside the implementation
- [ ] Add e2e test in `tests/e2e/`

#### Adding a new CRD field
- [ ] Update types in `api/v1alpha1/`
- [ ] Run `make generate-all` to regenerate deepcopy, CRDs, and sync Helm
- [ ] Update the relevant controller reconciler
- [ ] Update status conditions if needed
- [ ] Add controller unit tests
- [ ] Add e2e test coverage

#### Adding a new ext_proc handler
- [ ] Add handler in `internal/mcp-router/request_handlers.go` or `response_handlers.go`
- [ ] Update `server.go` processing logic
- [ ] Add OpenTelemetry span attributes for observability
- [ ] Add unit tests with mock ext_proc streams

#### Writing tests
- [ ] Unit tests use `testing` + `testify` or Ginkgo/Gomega
- [ ] E2E tests go in `tests/e2e/` using Ginkgo and are defined in a markdown file `tests/e2e/test_cases.md`
- [ ] E2E tests use direct port-forwards to `deployment/mcp-gateway`
- [ ] E2E tests clean up resources before creating them
- [ ] Test servers live in `tests/servers/` — create new ones for specific test scenarios

### Running Tests
```bash
make lint               # Run all lint and style checks
make test-unit          # Unit tests
make test-e2e-ci        # E2E tests for CI environment
make test-e2e           # E2E tests with local Kind cluster

# Running specific E2E tests locally
cd tests/e2e && go test -v -tags=e2e -run TestE2E -ginkgo.focus="test description" -timeout 5m

# Alternative with ginkgo CLI
ginkgo run -v --tags=e2e --focus="test description" tests/e2e/
```

### Version References
Docs and scripts on `main` always reference the latest published release version (plain SemVer, e.g., `0.5.1`). Git refs use a `v` prefix (e.g., `v${MCP_GATEWAY_VERSION}`), Helm `--version` uses bare SemVer. The `scripts/set-release-version.sh` script updates all version references and is run as part of the release process and the post-release bump on `main`.

## Reference

### Kubernetes Custom Resources
- MCPGatewayExtension: `docs/reference/mcpgatewayextension.md`
- MCPServerRegistration: `docs/reference/mcpserverregistration.md`
- MCPVirtualServer: `docs/reference/mcpvirtualserver.md`

### Important Ports
- 8080: Broker HTTP (/mcp endpoint)
- 50051: Router gRPC (ext_proc)
- 8081: Controller health probes
- 8001: Gateway port mapping
- 8002: Keycloak port mapping

### Configuration
- `config/crd/mcp.kuadrant.io_*.yaml`: CRD definitions (generated by controller-gen)
- `config/mcp-system/`: Kubernetes deployment manifests
- `config/test-servers/`: Test MCP server deployments
- `config/samples/remote-github/`: Example manifests for GitHub MCP integration

### Documentation
- `docs/CLAUDE.md`: Guidelines for writing and organizing documentation
- `docs/guides/`: User-facing how-to guides (published at docs.kuadrant.io)
- `docs/design/`: Developer-facing design docs and architecture

### Test Servers

Test servers in `config/test-servers/`:
- **Server1**: Go SDK (tools: greet, time, slow, headers)
- **Server2**: Go SDK (tools: hello_world, time, headers, auth1234, slow)
- **Server3**: Python FastMCP (tools: time, add, dozen, pi, get_weather, slow)
- **API Key Server**: Validates Bearer token authentication (tool: hello_world)
- **Broken Server**: Intentionally broken server for testing error handling
- **Custom Path Server**: Go SDK at `/v1/special/mcp` (tools: echo_custom, path_info, timestamp)
- **OIDC Server**: Validates OpenID Connect (OIDC) Bearer tokens
- **Everything Server**: Typescript SDK (prompts, tools, resources, sampling)
- **Conformance Server**: Typescript SDK conformance test server
- **Custom Response Server**: Tests custom response handling

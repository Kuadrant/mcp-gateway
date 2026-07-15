# Configuring MCP Protocol Modes

The MCP Gateway supports two protocol modes that control how the router handles MCP traffic. Each gateway instance operates in a single mode, selected via the `protocolMode` field on the MCPGatewayExtension resource.

## Protocol Modes

| Mode | Protocol Version | Routing | Sessions | Use When |
|------|-----------------|---------|----------|----------|
| **Stateful** (default) | 2025-11-25 | Body-parsed | Session-based (JWT) | Upstream servers use the 2025-11-25 protocol |
| **Stateless** | 2026-07-28 | Header-based (`Mcp-Method`, `Mcp-Name`) | None | Upstream servers support the 2026-07-28 protocol |

## Prerequisites

- MCP Gateway installed and running
- An existing MCPGatewayExtension resource

## Configuring Stateless Mode

### Step 1: Set the protocol mode

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1
kind: MCPGatewayExtension
metadata:
  name: my-gateway
  namespace: mcp-system
spec:
  targetRef:
    name: my-gateway
    namespace: gateway-system
    sectionName: mcp
  protocolMode: Stateless
EOF
```

### Step 2: Verify the deployment restarted

```bash
kubectl get deployment mcp-gateway -n mcp-system -o jsonpath='{.spec.template.spec.containers[0].command}' | grep protocol-mode
```

The output should contain `--protocol-mode=stateless`.

## Behaviour Differences

### Stateful mode (2025-11-25)

- Router parses the JSON-RPC body to determine method and tool/prompt name
- Gateway manages sessions: issues JWT-based `mcp-session-id`, maps gateway sessions to backend sessions
- Hairpin initialization: router initializes backend MCP server sessions via the gateway
- Elicitation ID rewriting: response body is streamed to rewrite elicitation IDs
- URL token elicitation: router resolves cached user tokens before forwarding

### Stateless mode (2026-07-28)

- Router reads `Mcp-Method` and `Mcp-Name` headers for routing decisions
- No session management — upstream servers handle state independently
- No hairpin initialization — broker calls `server/discover` directly
- Response pass-through — no body streaming or rewriting
- Header-body validation: router rejects requests where `Mcp-Name` header disagrees with the body `params.name` field (spec requirement)
- Prefix stripping still requires body access to rewrite the tool/prompt name

## Limitations in Stateless Mode

### Tool discovery and selection unavailable

The `discover_tools` and `select_tools` meta-tools are disabled in stateless mode. These tools use a session-scoped store to track which tools each client has selected — without sessions, there is no key to scope selections against.

In stateless mode, `tools/list` returns all registered tools without scope filtering. The discovery threshold (which hides tools until `select_tools` is called) is also inactive.

> **Note:** A future release will re-key the scope store by identity (`sub` claim from the authorization token) instead of session ID, enabling tool discovery and selection in stateless mode without sessions.

## Running Mixed Protocol Environments

A single gateway instance cannot serve both protocols simultaneously. To support clients using different protocol versions, deploy separate MCPGatewayExtension resources targeting different Gateway listeners:

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1
kind: MCPGatewayExtension
metadata:
  name: gateway-stateful
  namespace: mcp-stateful
spec:
  targetRef:
    name: my-gateway
    namespace: gateway-system
    sectionName: mcp-legacy
  protocolMode: Stateful
---
apiVersion: mcp.kuadrant.io/v1
kind: MCPGatewayExtension
metadata:
  name: gateway-stateless
  namespace: mcp-stateless
spec:
  targetRef:
    name: my-gateway
    namespace: gateway-system
    sectionName: mcp-2026
  protocolMode: Stateless
EOF
```

Each instance deploys its own broker-router and routes through its own listener.

## Next Steps

- [Register MCP Servers](register-mcp-servers.md) with the gateway
- [Configure Authentication](authentication.md) for your MCP servers
- [Understanding MCP Gateway Architecture](understanding-mcp-gateway-architecture.md) for how routing works

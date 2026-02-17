# MCP Gateway Request Flows


This document captures the main request flows that involve the MCP Gateway.

> note: Some show "no auth" this is to reduce noise and focus on the main flow through the gateway components.

## Table of Contents

- [Components](#components)
- [High Level Flow](#high-level-flows)
- [Initialize](#initialize)
- [Aggregated Tools/List](#aggregated-toolslist)
- [Tools/Call (no auth)](#toolscall-no-auth)
- [MCP Server Registration](#mcp-server-registration)
- [Auth](#auth)
  - [Tools/Call with auth (valid bearer token)](#toolscall-with-auth-valid-bearer-token)
  - [MCP Server Tool Call with Auth (Full auth flow)](#mcp-server-tool-call-with-auth-full-auth-flow)
- [MCP Notifications](#mcp-notifications)

## Components

| Component | Description |
|-----------|-------------|
| **Gateway** | Envoy proxy handling ingress traffic. Routes requests based on headers set by MCPRouter. |
| **MCPRouter** | Envoy external processor (ext_proc) that parses MCP requests, validates payloads, and sets routing headers (authority, x-mcp-tool, etc.). |
| **MCPBroker** | HTTP service that aggregates tools from multiple upstream MCP servers. Handles initialize and tools/list requests. |
| **MCPServer** | An upstream MCP server that provides tools. Receives tools/call requests routed by the Gateway. |
| **SessionCache** | In-memory or Redis based cache storing backend MCP server session IDs, keyed by gateway-session-id and server-name. |
| **WASM** | WebAssembly filter in Envoy that integrates with Authorino via Kuadrant for auth enforcement. |
| **Authorino** | Authentication and authorization service that validates JWTs and enforces AuthPolicy rules. |
| **AuthServer** | External OAuth/OIDC provider for client authentication. |

## High Level Flow

The flow below shows the common request flow with MCP Gateway. The MCPBackend represents either an actual MCPServer or the MCPBroker component. The type of request dictates which of these will be called in response to the MCP request.



```mermaid
sequenceDiagram
        title MCP Gateway Request Flow
        actor MCPClient
        MCPClient->>Gateway: MCP Request
        Gateway->>MCPRouter: MCP Request
        MCPRouter->>Gateway: Set headers and routing instructions
        Gateway->>Auth: Auth MCP Request <br/> (if configured)
        Auth->>Gateway: Auth response <br/> (ok/not ok)
        Gateway->>MCPClient: (not ok)
        note left of MCPRouter: (request auth ok)
        note right of Gateway: tools/call to the target MCPServer <br/> tools/list, initialize to the MCPBroker component <br/> tool list filtering is applied by the MCPBroker component.
        Gateway->>MCPBackend:   MCP Request
        MCPBackend->>MCPClient: MCP Response
```

## Initialize

```mermaid
sequenceDiagram
        title MCP Initialize Request Flow (no auth)
        actor MCPClient
        MCPClient->>Gateway: POST /mcp init
        Gateway->>MCPRouter: POST /mcp init
        MCPRouter->>Gateway: set headers
        Gateway->>MCPBroker: POST /mcp init
        note right of MCPBroker: MCPBroker is the default backend for /mcp
        MCPBroker->>MCPClient: set mcp-session-id
```

## Aggregated Tools/List

Auth is removed in this diagram. Auth is shown in larger diagrams below.

```mermaid
sequenceDiagram
  actor MCPClient
  participant Gateway as Gateway
  participant MCPRouter as MCPRouter
  participant MCPBroker as MCPBroker
  MCPClient->>Gateway: tools/list
  Gateway->>MCPRouter: tools/list
  MCPRouter->>Gateway: set headers
  Gateway->>MCPBroker: tools/list
  MCPBroker->>MCPClient: aggregated tools/list response
  note left of MCPBroker: list is built via discovery phase. <br/> The MCPBroker applies filtering to this list. <br/> via signed x-authorised-tools header <br/> and client specified x-mcp-virtualserver headers.
```

## Tools/Call

```mermaid
sequenceDiagram
        title MCP Tool Call
        actor MCPClient
        MCPClient->>Gateway: POST /mcp
        note right of MCPClient: method: tools/call
        Gateway->>MCPRouter: POST /mcp
        note left of MCPRouter: method: tools/call <br/> gateway mcp-session-id present <br/> payload validated
        MCPRouter->>SessionCache: get backend mcp server mcp-session-id
        SessionCache->>MCPRouter: no backend mcp server session found
        MCPRouter->>Gateway: initialize with client headers via gateway to ensure any additional auth applied
        Gateway->>MCPServer: initialize
        MCPServer->>MCPRouter: initialize response OK
        MCPRouter->>SessionCache: store backend mcp server mcp-session-id keyed against gateway-session-id/server-name
        MCPRouter->>Gateway: set header mcp-session-id
        MCPRouter->>Gateway: set header authority: <configured host>
        MCPRouter->>Gateway: update body to remove prefix (if needed)
        MCPRouter->>Gateway: set header x-mcp-tool header
        Gateway->>MCPServer: Route <configured host> Post /mcp tools/call
        MCPServer->>MCPClient: tools/call response
```

## MCP Server Registration

For detailed information on how MCP server registration works, including the MCPManager lifecycle and configuration change handling, see the [backend MCP Management doc](./backend-mcp-management.md).


## Auth

Below are some attempts with Auth in the mix.

## MCP Gateway Request Authentication

### Tools/Call with auth (valid bearer token)

```mermaid
sequenceDiagram
        title Simplified MCP Tool Call with Auth
        MCPClient->>Gateway: POST /mcp
        note right of MCPClient: method: tools/call <br/> name: prefix_echo
        Gateway->>MCPRouter: POST /mcp
        note left of MCPRouter: method: tools/call <br/> name: prefix_echo
        MCPRouter->>Gateway: set authority: <prefix>.<host>
        MCPRouter->>Gateway: update body to remove prefix
        MCPRouter->>Gateway: set x-mcp-tool, x-mcp-method header
        Gateway->>WASM: apply auth
        WASM->>Authorino: apply auth policy rules
        note right of Authorino: checking JWT, method and tool name access etc <br/> rules defined in AuthPolicy
        Authorino->>WASM: OK
        WASM->>Gateway: OK
        Gateway->>MCPServer: POST /mcp tools/call
```

In the above diagram we are showing the flow when a client has a valid bearer token. Below we have the full flow including the OAuth dance:

```mermaid
sequenceDiagram
        title MCP Initialize Request Flow (auth)
        actor MCPClient
        MCPClient->>Gateway: POST /mcp init
        Gateway->>MCPRouter: POST /mcp init
        MCPRouter->>Gateway: no routing needed
        Gateway->>WASM: POST /mcp init
        WASM->>Authorino: Apply Auth
        Authorino->>MCPClient: 401 WWW-Authenticate with resource meta-data
        note left of Authorino: WWW-Authenticate: Bearer <br/> resource_metadata=<host>/.well-known/oauth-protected-resource/mcp
        MCPClient->>Gateway: GET /.well-known/oauth-protected-resource/mcp
        Gateway->>MCPRouter: GET /.well-known/oauth-protected-resource/mcp
        MCPRouter->>Gateway: no routing needed
        Gateway->>MCPBroker: GET /.well-known/oauth-protected-resource/mcp
        MCPBroker->>MCPClient: responds with resource json with configured auth server etc
        MCPClient->>AuthServer: register
        MCPClient->>AuthServer: authenticate
        AuthServer->>MCPClient: authenticated !
        MCPClient->>Gateway: Bearer header set POST/mcp init
        Gateway->>MCPRouter: POST /mcp init
        MCPRouter->>Gateway: no routing needed
        Gateway->>WASM: POST /mcp init
        WASM->>Authorino: Apply Auth
        Authorino->>WASM: 200
        Gateway->>MCPBroker: POST /mcp init
        MCPBroker->>MCPClient: init response 200
```


## MCP Server Tool Call with Auth (Full auth flow)

```mermaid
sequenceDiagram
        title MCP Tool Call (auth)
        MCPClient->>Gateway: POST /mcp
        note right of MCPClient: method: tools/call <br/> name: prefix_echo
        Gateway->>MCPRouter: POST /mcp
        note left of MCPRouter: method: tools/call <br/> name: prefix_echo
        MCPRouter->>Gateway: set authority: <prefix>.<host>
        MCPRouter->>Gateway: update body to remove prefix
        MCPRouter->>Gateway: set x-mcp-tool header
        Gateway->>WASM: auth on authority
        WASM->>Authorino: apply auth
        note right of Authorino: checking JWT and tool name <br/> defined in AuthPolicy
        Authorino->>WASM: 401 WWW-Authenticate
        note left of Authorino: WWW-Authenticate: Bearer <br/> resource_metadata=<host>/.well-known/oauth-protected-resource/mcp
        WASM->>MCPClient: 401 WWW-Authenticate
        note left of WASM: WWW-Authenticate: Bearer <br/> resource_metadata=<host>/.well-known/oauth-protected-resource/mcp
        MCPClient->>Gateway: .well-known/oauth-protected-resource/mcp
        Gateway->>MCPRouter: .well-known/oauth-protected-resource/mcp
        Gateway->>MCPBroker: .well-known/oauth-protected-resource/mcp
        MCPBroker->>MCPClient: auth metadata response
        MCPClient->>AuthServer: Authenticate (dynamic client reg etc)
        AuthServer->>MCPClient: Authenticated !!
        MCPClient->>Gateway: Bearer header set POST/mcp
        note right of MCPClient: method: tools/call <br/> name: prefix_echo
        Gateway->>MCPRouter: POST /mcp tools/call
        MCPRouter->>Gateway: set authority: <prefix>.<host>
        MCPRouter->>Gateway: update body to remove prefix set headers etc
        Gateway->>WASM: POST /mcp tools/call
        WASM->>Authorino: Apply Auth
        Authorino->>WASM: OK
        Gateway->>MCPServer: POST /mcp tools/call
        MCPServer->>MCPClient: tools/call response
```

## MCP Notifications

For detailed information on how notifications work in the MCP Gateway, see the [notifications design documentation](./notifications.md).

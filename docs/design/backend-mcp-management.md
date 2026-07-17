## MCP Server Registration and Management 

### Problem

The MCP Gateway needs to discover and register backend MCP servers so that their tools can be aggregated and presented to clients as a unified MCP server. When an MCPServerRegistration custom resource is created or updated in Kubernetes, the MCP Gateway Controller must:

1. Discover the MCP server endpoint from the referenced HTTPRoute
2. Update the broker and router configuration
3. Initialize connections to the backend MCP server
4. Discover available tools
5. Register for state change notifications
6. Handle configuration changes
7. Periodically ensure that the backend MCP Server is alive and healthy

The broker needs a robust mechanism to manage the lifecycle of each upstream MCP server connection, including periodic health checks, reconnection logic, and graceful handling of configuration changes.

### Solution

The MCP Gateway uses a two-phase registration process:

1. **Controller Phase**: The MCP Gateway Controller watches for MCPServerRegistration resources, discovers server endpoints from HTTPRoutes, and writes aggregated configuration to ConfigMaps
2. **Broker Phase**: The MCP Broker reads configuration changes and manages each upstream MCP server through an `MCPManager` struct that handles the full lifecycle of the connection

Each upstream MCP server is managed by a dedicated `MCPManager` instance that runs as a background routine, handling:
- Initial connection and discovery
- Periodic health checks and reconnection
- State change notification subscriptions such as `notifications/tools/list_changed`
- Graceful shutdown

### Registration Flow

```mermaid
sequenceDiagram
  participant Controller as MCP Gateway Controller
  participant ConfigMap as ConfigMap
  participant Broker as MCP Broker
  participant Manager as MCPManager
  participant Server as MCP Server

  Note over Controller: Watch MCPServerRegistration resources
  Controller->>ConfigMap: Write aggregated config
  Note right of ConfigMap: Config includes:<br/>- Server URL<br/>- Hostname<br/>- Prefix<br/>- Credentials
  
  ConfigMap->>Broker: Config change notification
  Broker->>Broker: OnConfigChange()
  
  alt Server not registered
    Broker->>Broker: mcpManager := NewMCPManager(config)
    Broker->>Manager: Start() [background routine]
    Broker->>Broker: upstreamMCPS[id] = mcpManager
  else Server already registered
    Broker->>Broker: Check if config changed
    alt Config changed
      Broker->>Broker: upstreamMCPS[id].Stop()
        Broker->>Broker: mcpManager := NewMCPManager(config)
        Broker->>Manager: Start() [background routine]
        Broker->>Broker: upstreamMCPS[id] = mcpManager
    end
  end

  Note over Manager,Server: MCPManager lifecycle
  Manager->>Server: POST /mcp "initialize"
  Server->>Manager: initialize response
  Note right of Manager: Validate protocol version<br/>and capabilities
  Manager->>Server: POST /mcp "notifications/initialized"
  Manager->>Server: GET /mcp [supervised notification watcher]
  Note right of Manager: Watcher failure never fails<br/>the session; re-list backstops
  Manager->>Server: POST /mcp "tools/list"
  Server->>Manager: tools/list response
  Manager->>Broker: Register discovered tools
  Manager->>Manager: Periodic health checks and re-list
  Controller->>Broker: Fetch status updates /status
```

### MCPManager Responsibilities

The `MCPManager` is responsible for managing a single upstream MCP server connection. It handles:

1. **Initialization**: Establishes connection, validates protocol version and capabilities
2. **Discovery**: Fetches initial tool list and registers tools with the broker
3. **Health Monitoring**: Periodically checks connection health, re-lists tools/prompts, and reconnects if needed
4. **Notification Handling**: Reacts to `list_changed` notifications from the notification watcher with an immediate re-list
5. **Graceful Shutdown**: Cleans up connections, including the notification watcher, when stopped

### Upstream Freshness

The MCP Go SDK opens the standalone GET SSE stream synchronously inside `Connect` on a detached context and treats its failure as session-fatal, so a single upstream that mishandles the GET would block connection establishment for minutes and then poison the session. The SDK therefore never owns that stream (`DisableStandaloneSSE` is set, matching the router's hairpin client).

Instead, each connected session runs a broker-owned **notification watcher** that holds the standalone GET stream with the broker's failure semantics:

- Failures are never fatal to the session: the watcher retries forever with capped exponential backoff (aligned with the manager's backoff)
- A `405`/`404` response, or a `200` without an SSE content type, means the upstream does not offer the stream: the watcher stops permanently, the session is unaffected
- `notifications/tools/list_changed` and `notifications/prompts/list_changed` trigger an immediate re-list through the manager's existing refresh path
- Server pings delivered on the stream are answered, so keepalive-enabled upstreams do not consider the broker session dead
- The watcher resumes with `Last-Event-ID` when the upstream supplies event ids
- The watcher uses the same HTTP client as all other upstream calls (auth headers, TLS trust pool, response header timeout)

The manager additionally re-lists tools and prompts on every health tick regardless of the upstream's `listChanged` capability. This poll backstop is deliberate: upstreams without event replay do not buffer notifications sent while the stream is down (reconnect windows lose events), and the watcher stops permanently for upstreams that do not offer the stream at all. Push keeps updates immediate; the tick bounds worst-case staleness at the ticker interval (default 1 minute) in every failure mode.

For `userSpecificList` servers the manager's health session still starts a watcher like any other connected session; only the per-user sessions do not run one, as their tool lists are fetched per request, leaving no cached state for a notification to refresh and nothing consuming server pushes.

For client-facing notification forwarding, see the [notifications design documentation](./notifications.md).

### Error Handling and Retry Logic

The MCPManager implements exponential backoff retry for:
- Initial connection failures
- Ping (health check) failures
- Discovery failures

On connection or ping failure the manager keeps serving its cached tools and prompts, dropping them only after three consecutive failed checks (`maxConsecutiveFailures`), avoiding client-visible tool churn on transient blips. Retries are handled in background routines to avoid blocking the main broker operations.

### Status and Health

The broker exposes status information about registered servers via its `/status` endpoint:
- Connection status
- Last successful discovery time
- Tool count
- Error messages (if any)

This information is for debugging and monitoring only. It is not surfaced in the MCPServerRegistration CRD status. The controller considers a registration ready once the config secret has been written successfully.

# OpenTelemetry Observability Stack

This directory contains Kubernetes manifests for deploying an OpenTelemetry observability stack for local development and testing.

## Components

| Component | Purpose | Port |
|-----------|---------|------|
| **OTEL Collector** | Receives traces and logs, routes to backends | 4317 (gRPC), 4318 (HTTP) |
| **Tempo** | Trace storage and query | 3200 (HTTP), 4317 (OTLP) |
| **Loki** | Log storage and query (with trace correlation) | 3100 |
| **Grafana** | Visualization and dashboards | 3000 |

## Setup Flow

The observability stack integrates with the MCP Gateway, Istio, and optionally Kuadrant/Authorino.

### Full distributed tracing (Istio + Authorino + MCP Gateway)

```bash
make local-env-setup                          # 1. Create Kind cluster
make otel ISTIO_TRACING=1 AUTH_TRACING=1      # 2. Deploy OTEL + auth stack + enable all tracing
make otel-status                              # 3. Check status of OTEL stack
make otel-forward                             # 4. Port-forward Grafana
make otel-delete                              # 5. Cleanup
```

When `AUTH_TRACING=1` is set, `make otel` will automatically install the auth stack
(cert-manager, Kuadrant, Keycloak) if not already present via `make auth-example-setup`.

When `AUTH_TRACING=1` is set, `make otel` will also:
1. Install Prometheus Operator CRDs (ServiceMonitor and PodMonitor) -- required by the
   kuadrant-operator `ObservabilityReconciler`
2. Patch the Kuadrant CR with `spec.observability.tracing.defaultEndpoint` pointing to the
   OTEL Collector
3. Restart the kuadrant-operator so it can reconcile cleanly and create:
   - A `tracing-service` entry in the WasmPlugin `pluginConfig.services`
   - An `observability.tracing` section in the WasmPlugin config
   - A `kuadrant-tracing-*` EnvoyFilter for the tracing cluster


   
## MCP Router Spans

The MCP Router (ext_proc) emits the following spans. All spans are children of the root
`mcp-router.process` span, which covers the full ext_proc stream lifecycle for a single
request (request headers -> request body -> response headers).

| Span | When | Description |
|------|------|-------------|
| `mcp-router.process` | Every ext_proc stream | Root span. Starts when request headers arrive, ends after response headers are processed. |
| `mcp-router.route-decision` | Request body parsed | Routing decision: tool-call, prompt-get, elicitation-response, or broker. |
| `mcp-router.broker-passthrough` | Non-routed requests | Pass-through to broker (initialize, tools/list, prompts/list, notifications). |
| `mcp-router.tool-call` | `tools/call` requests | Full tool call handling including session and server resolution. |
| `mcp-router.broker.get-server-info` | Inside tool-call | Resolve which backend server owns the tool. |
| `mcp-router.prompt-get` | `prompts/get` requests | Full prompt get handling including session and server resolution. |
| `mcp-router.broker.get-server-info-by-prompt` | Inside prompt-get | Resolve which backend server owns the prompt. |
| `mcp-router.elicitation-response` | Elicitation responses | Routes client elicitation responses to the correct backend server. |
| `mcp-router.session-cache.get` | Inside tool-call or prompt-get | Look up an existing backend session in the cache. |
| `mcp-router.session-init` | Cache miss | Hairpin initialize request through the gateway to the backend MCP server. |
| `mcp-router.session-cache.store` | After session-init | Store the new backend session in the cache. |

### MCP Broker Spans

The MCP Broker emits spans for request handling, capability filtering, and upstream management.

| Span | When | Description |
|------|------|-------------|
| `mcp-broker.handle-request` | Every MCP request to the broker | Wraps the full request lifecycle (initialize, tools/list, prompts/list, etc.). |
| `mcp-broker.tools-list` | `tools/list` response filtering | Filters tools by authorization, virtual server, and removes gateway metadata. |
| `mcp-broker.prompts-list` | `prompts/list` response filtering | Filters prompts by authorization, virtual server, and removes gateway metadata. |
| `mcp-broker.upstream-manage` | Periodic health check tick | Backend connection management: connect, ping, tool/prompt discovery. |

### Span Hierarchy

The router and broker run in the same process. Broker spans are correlated to the
router trace via W3C Trace Context propagation (the `traceContextMiddleware` extracts
`traceparent` from the forwarded HTTP request), so they appear in the same trace but
are **not** direct parent-child spans of the router spans.

```text
mcp-router.process
├── mcp-router.route-decision
│   ├── mcp-router.broker-passthrough        (if initialize, tools/list, prompts/list, etc.)
│   ├── mcp-router.tool-call                 (if tools/call)
│   │   ├── mcp-router.broker.get-server-info
│   │   ├── mcp-router.session-cache.get
│   │   ├── mcp-router.session-init          (if cache miss)
│   │   └── mcp-router.session-cache.store   (if cache miss)
│   ├── mcp-router.prompt-get                (if prompts/get)
│   │   ├── mcp-router.broker.get-server-info-by-prompt
│   │   ├── mcp-router.session-cache.get
│   │   ├── mcp-router.session-init          (if cache miss)
│   │   └── mcp-router.session-cache.store   (if cache miss)
│   └── mcp-router.elicitation-response      (if elicitation response)

mcp-broker.handle-request                    (correlated via traceparent, not a child of router spans)
├── mcp-broker.tools-list                    (if tools/list)
└── mcp-broker.prompts-list                  (if prompts/list)

mcp-broker.upstream-manage                   (periodic, not request-scoped)
```

### Span Attributes

Attributes follow the [OpenTelemetry MCP Semantic Conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/mcp/#server).

#### Root span (`mcp-router.process`)

| Attribute | Source | Description |
|-----------|--------|-------------|
| `http.method` | `:method` header | HTTP method (POST) |
| `http.path` | `:path` header | Request path (/mcp) |
| `http.request_id` | `x-request-id` header | Envoy request ID |
| `mcp.method.name` | JSON-RPC `method` field | MCP method (initialize, tools/call, tools/list, etc.) |
| `gen_ai.tool.name` | JSON-RPC `params.name` | Tool name (only for tools/call) |
| `jsonrpc.request.id` | JSON-RPC `id` field | JSON-RPC request ID |
| `jsonrpc.protocol.version` | JSON-RPC `jsonrpc` field | Always "2.0" |
| `gen_ai.operation.name` | JSON-RPC `method` field | Same as mcp.method.name |
| `mcp.session.id` | `mcp-session-id` header | Gateway session ID |
| `client.address` | `x-forwarded-for` header | Client IP address |
| `http.status_code` | `:status` response header | Response status code |

#### Route decision span (`mcp-router.route-decision`)

| Attribute | Description |
|-----------|-------------|
| `mcp.method.name` | MCP method |
| `mcp.route` | Routing decision: `tool-call`, `prompt-get`, `elicitation-response`, or `broker` |

#### Tool call span (`mcp-router.tool-call`)

| Attribute | Description |
|-----------|-------------|
| `gen_ai.tool.name` | Tool name from the request |
| `mcp.session.id` | Gateway session ID |
| `mcp.server` | Resolved backend server name |
| `mcp.server.hostname` | Resolved backend server hostname |

#### Prompt get span (`mcp-router.prompt-get`)

| Attribute | Description |
|-----------|-------------|
| `mcp.prompt.name` | Prompt name from the request |
| `mcp.session.id` | Gateway session ID |
| `mcp.server` | Resolved backend server name |
| `mcp.server.hostname` | Resolved backend server hostname |

#### Broker spans

| Attribute | Span | Description |
|-----------|------|-------------|
| `mcp.method` | `handle-request` | MCP method being processed |
| `mcp.session.id` | `handle-request`, `tools-list`, `prompts-list` | Gateway session ID |
| `mcp.tools.count` | `tools-list` | Number of tools after filtering |
| `mcp.prompts.count` | `prompts-list` | Number of prompts after filtering |
| `mcp.server` | `upstream-manage` | Backend server name |

#### Error attributes

On error, spans include:

| Attribute | Description |
|-----------|-------------|
| `error.type` | Error classification (e.g. `tool_not_found`, `prompt_not_found`, `invalid_session`, `session_cache_error`) |
| `error_source` | Component that generated the error (`ext-proc` or `backend`) |
| `http.status_code` | HTTP status code returned |

## Configuration

### Environment Variables

The MCP Gateway reads these environment variables to configure OpenTelemetry export.
When no endpoint is configured, OTel is completely disabled (zero overhead).

| Variable | Description | Default |
|----------|-------------|---------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Base OTLP endpoint for all signals | (none - disabled) |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | Override endpoint for traces | Falls back to base |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | Override endpoint for logs | Falls back to base |
| `OTEL_EXPORTER_OTLP_INSECURE` | Disable TLS | `false` |
| `OTEL_SERVICE_NAME` | Service name in traces | `mcp-gateway` |
| `OTEL_SERVICE_VERSION` | Service version | Build version |

### Manual Configuration (without make targets)

```bash
kubectl set env deployment/mcp-gateway -n mcp-system \
  OTEL_EXPORTER_OTLP_ENDPOINT="http://your-collector:4318" \
  OTEL_EXPORTER_OTLP_INSECURE="true"
```

### Trace Context Propagation

The router extracts W3C Trace Context (`traceparent` header) from incoming Envoy
headers. When Istio tracing is enabled, Envoy injects this header automatically,
so router spans join the Istio trace. You can also set `traceparent` manually from
outside the mesh to create end-to-end traces.

## Architecture

```text
┌─────────────────┐     ┌──────────────────────┐     ┌─────────────┐
│   MCP Gateway   │────▶│   OTEL Collector     │────▶│    Tempo    │
│                 │     │                      │     │  (traces)   │
│ OTEL_EXPORTER_  │     │  Receives OTLP       │     └─────────────┘
│ OTLP_ENDPOINT=  │     │  Routes to backends  │
│ http://otel-    │     │                      │     ┌─────────────┐
│ collector:4318  │     │                      │────▶│    Loki     │
└─────────────────┘     │                      │     │   (logs)    │
                        └──────────────────────┘     └─────────────┘
                                                             │
                                                             ▼
                                                      ┌─────────────┐
                                                      │   Grafana   │
                                                      │  (query UI) │
                                                      └─────────────┘
```

## Testing

### Generate Traffic (no auth)

```bash
# 1. Initialize MCP session
curl -s -D /tmp/mcp_headers -X POST http://localhost:8001/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "test-client", "version": "1.0.0"}}}'

# 2. Extract session ID
SESSION_ID=$(grep -i "mcp-session-id:" /tmp/mcp_headers | cut -d' ' -f2 | tr -d '\r')
echo "Session ID: $SESSION_ID"

# 3. List tools
curl -s -X POST http://localhost:8001/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SESSION_ID" \
  -d '{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}'

# 4. Call a tool
curl -s -X POST http://localhost:8001/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SESSION_ID" \
  -d '{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "test2_hello_world", "arguments": {"name": "World"}}}'

# Cleanup
rm -f /tmp/mcp_headers
```

### Generate Traffic with Trace Propagation

Pass a `traceparent` header to create a known trace ID you can search for in Tempo:

```bash
TRACE_ID=$(openssl rand -hex 16)
echo "Trace ID: $TRACE_ID"

curl -s -D /tmp/mcp_headers -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "traceparent: 00-${TRACE_ID}-$(openssl rand -hex 8)-01" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "test-client", "version": "1.0.0"}}}'

SESSION_ID=$(grep -i "mcp-session-id:" /tmp/mcp_headers | cut -d' ' -f2 | tr -d '\r')

curl -s -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SESSION_ID" \
  -H "traceparent: 00-${TRACE_ID}-$(openssl rand -hex 8)-01" \
  -d '{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}'

curl -s -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SESSION_ID" \
  -H "traceparent: 00-${TRACE_ID}-$(openssl rand -hex 8)-01" \
  -d '{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "test2_headers"}}'

echo "Search for trace: $TRACE_ID"
```

### Generate Authenticated Traffic (AUTH_TRACING=1)

#### Prerequisites

Enable direct access grants on the Keycloak client:

```bash
ADMIN_TOKEN=$(curl -sk -X POST \
  https://keycloak.127-0-0-1.sslip.io:8002/realms/master/protocol/openid-connect/token \
  -d "grant_type=password" -d "client_id=admin-cli" \
  -d "username=admin" -d "password=admin" | jq -r .access_token)

CLIENT_UUID=$(curl -sk \
  "https://keycloak.127-0-0-1.sslip.io:8002/admin/realms/mcp/clients?clientId=mcp-gateway" \
  -H "Authorization: Bearer $ADMIN_TOKEN" | jq -r '.[0].id')

curl -sk -X PUT \
  "https://keycloak.127-0-0-1.sslip.io:8002/admin/realms/mcp/clients/$CLIENT_UUID" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"clientId":"mcp-gateway","directAccessGrantsEnabled":true}'
```

#### Authenticated requests

```bash
# 1. Get access token
ACCESS_TOKEN=$(curl -sk -X POST \
  https://keycloak.127-0-0-1.sslip.io:8002/realms/mcp/protocol/openid-connect/token \
  -d "grant_type=password" \
  -d "client_id=mcp-gateway" \
  -d "client_secret=secret" \
  -d "username=mcp" \
  -d "password=mcp" \
  -d "scope=openid groups roles" | jq -r .access_token)

# 2. Generate trace ID
TRACE_ID=$(openssl rand -hex 16)
echo "Trace ID: $TRACE_ID"

# 3. Initialize
curl -s -D /tmp/mcp_headers -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "traceparent: 00-${TRACE_ID}-$(openssl rand -hex 8)-01" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "curl-client", "version": "1.0"}}}'

SESSION_ID=$(grep -i "mcp-session-id:" /tmp/mcp_headers | cut -d' ' -f2 | tr -d '\r')
echo "Session ID: $SESSION_ID"

# 4. List tools
curl -s -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "mcp-session-id: $SESSION_ID" \
  -H "traceparent: 00-${TRACE_ID}-$(openssl rand -hex 8)-01" \
  -d '{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}'

# 5. Call a tool
curl -s -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "mcp-session-id: $SESSION_ID" \
  -H "traceparent: 00-${TRACE_ID}-$(openssl rand -hex 8)-01" \
  -d '{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "test2_headers"}}'

echo "Search for trace: $TRACE_ID"
```

### View Traces in Tempo

1. Open http://localhost:3000
2. Go to **Explore** (compass icon in left sidebar)
3. Select **Tempo** as the datasource
4. Click **Search** tab
5. Set Service Name to `mcp-gateway`
6. Click **Run query**
7. Click on a trace to see the span waterfall

### View Logs with Trace Correlation

1. In Grafana, go to **Explore**
2. Select **Loki** as the datasource
3. Enter query: `{job="mcp-gateway"}`
4. Expand a log line -- look for `trace_id` and `span_id` fields
5. Click the `trace_id` value to jump directly to that trace in Tempo

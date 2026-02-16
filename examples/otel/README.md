# OpenTelemetry Observability Stack

This directory contains Kubernetes manifests for deploying an OpenTelemetry observability stack for local development and testing.

## Prerequisites

Set up a local Kind cluster with MCP Gateway:

```bash
make local-env-setup
```

## Components

| Component | Purpose | Port |
|-----------|---------|------|
| **OTEL Collector** | Receives traces/metrics/logs, routes to backends | 4317 (gRPC), 4318 (HTTP) |
| **Tempo** | Trace storage and query | 3200 (HTTP), 4317 (OTLP) |
| **Loki** | Log storage and query (with trace correlation) | 3100 |
| **Prometheus** | Metrics storage | 9090 |
| **Grafana** | Visualization and dashboards | 3000 |

## Quick Start

```bash
# Deploy the stack
make otel

# Deploy with Istio/Envoy & Authorino tracing enabled
make otel ISTIO_TRACING=1 AUTH_TRACING=1

# Deploy with all tracing including Kuadrant wasm-shim spans
make otel ISTIO_TRACING=1 AUTH_TRACING=1 WASM_TRACING=1

# Check status
make otel-status

# Port-forward Grafana and Prometheus
make otel-forward
```

## Setup Flow

The observability stack integrates with the MCP Gateway, Istio, and optionally Kuadrant/Authorino.
The setup order matters.

### MCP Gateway tracing only (no Istio/Authorino spans)

```bash
make local-env-setup   # 1. Create Kind cluster with Istio, Gateway, MCP Gateway
make otel              # 2. Deploy OTEL stack, configure broker to export traces
make otel-forward      # 3. Port-forward Grafana (3000), Prometheus (9090)
```

This gives you traces from the MCP broker/router only. Istio (Envoy) and Authorino
spans will NOT appear in Tempo.

### Full distributed tracing (Istio + Authorino + MCP Gateway)

```bash
make local-env-setup                          # 1. Create Kind cluster
make otel ISTIO_TRACING=1 AUTH_TRACING=1      # 2. Deploy OTEL + auth stack + enable all tracing
make otel-forward                             # 3. Port-forward Grafana
```

When `AUTH_TRACING=1` is set, `make otel` will automatically install the auth stack
(cert-manager, Kuadrant, Keycloak) if not already present via `make auth-example-setup`.

### Full distributed tracing with wasm-shim spans

```bash
make local-env-setup                                              # 1. Create Kind cluster
make otel ISTIO_TRACING=1 AUTH_TRACING=1 WASM_TRACING=1           # 2. Deploy everything + wasm-shim tracing
make otel-forward                                                 # 3. Port-forward Grafana
```

When `WASM_TRACING=1` is set, `make otel` will:
1. Install Prometheus Operator CRDs (ServiceMonitor and PodMonitor) -- required by the
   kuadrant-operator `ObservabilityReconciler` even when Prometheus is not deployed
2. Patch the Kuadrant CR with `spec.observability.tracing.defaultEndpoint` pointing to the
   OTEL Collector
3. Restart the kuadrant-operator so it can reconcile cleanly and create:
   - A `tracing-service` entry in the WasmPlugin `pluginConfig.services`
   - An `observability.tracing` section in the WasmPlugin config
   - A `kuadrant-tracing-*` EnvoyFilter for the tracing cluster

This produces spans from the Kuadrant wasm-shim itself (rate limiting decisions, auth policy
evaluations) alongside existing Envoy, Authorino, and MCP Gateway spans.

### Adding tracing to an existing auth setup

If you already ran `make auth-example-setup` separately:

```bash
make otel AUTH_TRACING=1                      # Safe to run -- will not reinstall auth stack
make otel AUTH_TRACING=1 ISTIO_TRACING=1      # Also enable Envoy spans
make otel AUTH_TRACING=1 WASM_TRACING=1       # Also enable wasm-shim spans
```

### Flags

| Flag | Effect |
|------|--------|
| `ISTIO_TRACING=1` | Configures Istio mesh to send Envoy traces to OTEL Collector via extensionProviders |
| `AUTH_TRACING=1` | Installs auth stack (if needed) and patches Authorino to send traces to OTEL Collector |
| `WASM_TRACING=1` | Installs Prometheus Operator CRDs and patches Kuadrant CR for wasm-shim tracing |

`ISTIO_TRACING` and `AUTH_TRACING` are independent. `WASM_TRACING` requires
the Kuadrant operator to be installed (use with `AUTH_TRACING=1` or after
running `make auth-example-setup`).

> **Note:** All `make otel` operations are idempotent -- safe to re-run without creating
> duplicate configuration.

## Architecture

```
┌─────────────────┐     ┌──────────────────────┐     ┌─────────────┐
│   MCP Gateway   │────▶│   OTEL Collector     │────▶│    Tempo    │
│                 │     │                      │     │  (traces)   │
│ OTEL_EXPORTER_  │     │  Receives OTLP       │     └─────────────┘
│ OTLP_ENDPOINT=  │     │  Routes to backends  │
│ http://otel-    │     │                      │     ┌─────────────┐
│ collector:4318  │     │                      │────▶│    Loki     │
└─────────────────┘     │                      │     │   (logs)    │
                        │                      │     └─────────────┘
                        │                      │
                        │                      │     ┌─────────────┐
                        │                      │────▶│ Prometheus  │
                        └──────────────────────┘     │  (metrics)  │
                                                      └─────────────┘
                                                             │
                                                             ▼
                                                      ┌─────────────┐
                                                      │   Grafana   │
                                                      │  (query UI) │
                                                      └─────────────┘
```

## Testing Guide

### Step 1: Generate Traffic

```bash
# Step 1: Initialize MCP session and capture session ID
curl -s -D /tmp/mcp_headers -X POST http://localhost:8001/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "test-client", "version": "1.0.0"}}}'

# Extract the session ID from response headers
SESSION_ID=$(grep -i "mcp-session-id:" /tmp/mcp_headers | cut -d' ' -f2 | tr -d '\r')
echo "Session ID: $SESSION_ID"

# Step 2: List tools using the session ID
curl -X POST http://localhost:8001/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SESSION_ID" \
  -d '{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}'

# Step 3: Call a tool (if test servers are deployed)
curl -X POST http://localhost:8001/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SESSION_ID" \
  -d '{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "test2_hello_world", "arguments": {"name": "Patryk"}}}'

# Cleanup
rm -f /tmp/mcp_headers
```

or using npx 

```bash
TRACEPARENT="00-$(openssl rand -hex 16)-$(openssl rand -hex 8)-01";npx @modelcontextprotocol/inspector --cli http://mcp.127-0-0-1.sslip.io:8001/mcp --transport http --method tools/call --tool-name test2_headers --header "traceparent:$TRACEPARENT" && echo $TRACEPARENT
TRACEPARENT="00-$(openssl rand -hex 16)-$(openssl rand -hex 8)-01";npx @modelcontextprotocol/inspector --cli http://mcp.127-0-0-1.sslip.io:8001/mcp --transport http --method tools/call --tool-name test2_hello_world --tool-arg name=Patryk --header "traceparent:$TRACEPARENT" && echo $TRACEPARENT

```

### Generate Authenticated Traffic with Trace Propagation

When `AUTH_TRACING=1` is enabled, you can test the full trace path through Istio, Authorino, and MCP Gateway.

#### Prerequisites

The `mcp-gateway` Keycloak client needs `directAccessGrantsEnabled` to obtain tokens via curl.
Enable it through the Keycloak admin console:

1. Open https://keycloak.127-0-0-1.sslip.io:8002/admin (login: `admin` / `admin`)
2. Select the **mcp** realm
3. Go to **Clients** → **mcp-gateway** → **Settings**
4. Enable **Direct access grants** and save

Or via the admin API:

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

#### Authenticated MCP Requests with Trace Context

```bash
# 1. Get an access token from Keycloak
ACCESS_TOKEN=$(curl -sk -X POST \
  https://keycloak.127-0-0-1.sslip.io:8002/realms/mcp/protocol/openid-connect/token \
  -d "grant_type=password" \
  -d "client_id=mcp-gateway" \
  -d "client_secret=secret" \
  -d "username=mcp" \
  -d "password=mcp" \
  -d "scope=openid groups roles" | jq -r .access_token)

# 2. Generate a trace ID to follow through Grafana/Tempo
TRACE_ID=$(openssl rand -hex 16)
echo "Trace ID: $TRACE_ID"

# 3. Initialize MCP session with traceparent header
curl -s -D /tmp/mcp_headers -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "traceparent: 00-${TRACE_ID}-$(openssl rand -hex 8)-01" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "initialize",
    "params": {
      "protocolVersion": "2025-06-18",
      "capabilities": {},
      "clientInfo": {"name": "curl-client", "version": "1.0"}
    }
  }' | jq .

# 4. Extract session ID
SESSION_ID=$(grep -i "mcp-session-id:" /tmp/mcp_headers | cut -d' ' -f2 | tr -d '\r')
echo "Session ID: $SESSION_ID"

# 5. List tools (same trace ID, new span)
curl -s -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "mcp-session-id: $SESSION_ID" \
  -H "traceparent: 00-${TRACE_ID}-$(openssl rand -hex 8)-01" \
  -d '{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}' | jq .

# 6. Call a tool
curl -s -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "mcp-session-id: $SESSION_ID" \
  -H "traceparent: 00-${TRACE_ID}-$(openssl rand -hex 8)-01" \
  -d '{
    "jsonrpc": "2.0",
    "id": 3,
    "method": "tools/call",
    "params": {"name": "test2_headers"}
  }' | jq .
```

The `traceparent` header follows the [W3C Trace Context](https://www.w3.org/TR/trace-context/) standard. By setting it on the initial request from outside the mesh, the trace ID propagates through Istio's ingress gateway, Authorino's auth evaluation, and into the MCP Gateway. Search for the printed `Trace ID` in Tempo to see the full distributed trace.

#### Using MCP Inspector

Use the MCP Inspector for browser-based authenticated traffic (handles OAuth flow automatically):

```bash
make inspect-gateway
```

**Test Credentials**: `mcp` / `mcp`

### Step 2: View Traces in Tempo

1. Open http://localhost:3000
2. Go to **Explore** (compass icon in left sidebar)
3. Select **Tempo** as the datasource
4. Click **Search** tab
5. Set Service Name to `mcp-gateway`, `authorino`, or `wasm-shim`
6. Click **Run query**
7. Click on a trace to see the span waterfall

### Step 3: View Logs with Trace IDs

1. In Grafana, go to **Explore**
2. Select **Loki** as the datasource
3. Enter query: `{job="mcp-gateway"}`
4. Click **Run query**
5. Expand a log line - look for `traceid` and `spanid` fields
6. Click the `traceid` value to jump directly to that trace in Tempo

## Configuration

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Base OTLP endpoint | (none - disabled) |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | Override for traces | Falls back to base |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | Override for metrics | Falls back to base |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | Override for logs | Falls back to base |
| `OTEL_EXPORTER_OTLP_INSECURE` | Disable TLS | `false` |
| `OTEL_SERVICE_NAME` | Service name in traces | `mcp-gateway` |
| `OTEL_SERVICE_VERSION` | Service version | Build version |



## Cleanup

```bash
make otel-delete
```

## Files

| File | Description |
|------|-------------|
| `namespace.yaml` | Creates `observability` namespace |
| `otel-collector.yaml` | Collector deployment, service, and config |
| `tempo.yaml` | Tempo deployment, service, and config |
| `loki.yaml` | Loki deployment, service, and config |
| `prometheus.yaml` | Prometheus deployment, service, and config |
| `grafana.yaml` | Grafana deployment with preconfigured datasources |
| `istio-telemetry.yaml` | Istio Telemetry and DestinationRule for distributed tracing |

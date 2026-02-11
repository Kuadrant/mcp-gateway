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

# Deploy with Istio/Envoy distributed tracing enabled
make otel ISTIO_TRACING=1

# Check status
make otel-status

# Port-forward Grafana and Prometheus
make otel-forward
```

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

### Step 2: View Traces in Tempo

1. Open http://localhost:3000
2. Go to **Explore** (compass icon in left sidebar)
3. Select **Tempo** as the datasource
4. Click **Search** tab
5. Set Service Name to `mcp-gateway`
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
| `istio-telemetry.yaml` | Istio Telemetry and Sail operator config for distributed tracing |

# OpenTelemetry Observability Stack

This directory contains Kubernetes manifests for deploying an OpenTelemetry observability stack for local development and testing.

## Components

| Component | Purpose | Port |
|-----------|---------|------|
| **OTEL Collector** | Receives traces and logs, routes to backends | 4317 (gRPC), 4318 (HTTP) |
| **Tempo** | Trace storage and query | 3200 (HTTP), 4317 (OTLP) |
| **Loki** | Log storage and query (with trace correlation) | 3100 |
| **Grafana** | Visualization and dashboards | 3000 |
| **Prometheus** | Optional standalone scraper for gateway and Istio metrics | 9090 |

## Setup Flow

The observability stack integrates with the MCP Gateway, Istio, and optionally Kuadrant/Authorino.

### Full distributed tracing (Istio + Authorino + MCP Gateway)

```bash
make local-env-setup                          # 1. Create Kind cluster
make otel ISTIO_TRACING=1 AUTH_TRACING=1      # 2. Deploy OTEL + auth stack and enable tracing
kubectl apply -f examples/otel/prometheus.yaml # 3. Optional: deploy standalone Prometheus
make otel-status                              # 4. Check status of the OTEL stack
make otel-forward                             # 5. Port-forward Grafana only
make otel-delete                              # 6. Tear down the OTEL stack
```

`make otel` applies exactly these local stack manifests: `namespace.yaml`, `tempo.yaml`,
`loki.yaml`, `otel-collector.yaml`, and `grafana.yaml`. It waits for the deployments in
the `observability` namespace, configures the gateway to export OTLP telemetry to the
collector over HTTP, and then waits for the gateway rollout. It does **not** deploy
standalone Prometheus.

Apply the standalone Prometheus deployment separately when you want the provisioned
Grafana Prometheus datasource and MCP Gateway metrics dashboard:

```bash
kubectl apply -f examples/otel/prometheus.yaml
```

That manifest creates a regular Prometheus `Deployment`, `Service`, service account, and
RBAC. It scrapes MCP Gateway pods on port 9090 and Istio gateway pods on port 15090. It
is not a Prometheus Operator installation and does not create or require
`ServiceMonitor` or `PodMonitor` resources. An external Prometheus can be used instead,
but it must have equivalent scrape targets and the Grafana Prometheus datasource must
point to that Prometheus instance.

When `ISTIO_TRACING=1` is set, `make otel` applies `istio-telemetry.yaml` and patches the
Istio default mesh configuration to enable tracing and send spans to the collector.

When `AUTH_TRACING=1` is set, `make otel` will automatically install the auth example
(cert-manager, Kuadrant, Keycloak) if it is not already present via
`make auth-example-setup`. It also:

1. Patches Authorino to export tracing to the OTEL collector.
2. Applies the Prometheus Operator `ServiceMonitor` and `PodMonitor` CRDs required by
   the Kuadrant `ObservabilityReconciler`. These CRDs are separate from the standalone
   Prometheus deployment described above.
3. Patches the Kuadrant CR with `spec.observability.tracing.defaultEndpoint` pointing to
   the OTEL Collector.
4. Restarts the kuadrant-operator so it can reconcile cleanly and create:
   - A `tracing-service` entry in the WasmPlugin `pluginConfig.services`
   - An `observability.tracing` section in the WasmPlugin config
   - A `kuadrant-tracing-*` EnvoyFilter for the tracing cluster

### Teardown and cleanup limitations

`make otel-delete` deletes the observability namespace resources (Grafana, the collector,
Loki, and Tempo), deletes the Istio telemetry resource, and resets Istio mesh tracing.
It does not remove the OTLP environment variables from the gateway deployment. Remove
them separately, using the namespace for the local gateway:

```bash
kubectl set env deployment/mcp-gateway -n <MCP_GATEWAY_NAMESPACE> \
  OTEL_EXPORTER_OTLP_ENDPOINT- \
  OTEL_EXPORTER_OTLP_TRACES_ENDPOINT- \
  OTEL_EXPORTER_OTLP_LOGS_ENDPOINT- \
  OTEL_EXPORTER_OTLP_INSECURE-
```

If the optional Prometheus manifest was applied, its namespaced objects are removed when
the `observability` namespace is deleted, but `make otel-delete` does not explicitly
delete that manifest's cluster-scoped `prometheus-mcp` role and role binding. Remove
those separately if a completely clean cluster is required.

`AUTH_TRACING=1` also changes Authorino and Kuadrant tracing configuration. `make
otel-delete` does not reverse those patches, remove the auth example installed by
`auth-example-setup`, or remove the Prometheus Operator CRDs.

## Telemetry reference

The [OpenTelemetry integration guide](../../docs/guides/opentelemetry.md) is the
canonical reference for gateway configuration, spans, attributes, errors, trace
propagation, and metrics. This README covers only the local observability stack,
traffic generation, and Grafana workflows.

## Architecture

```text
                                      ┌─────────────┐
                                      │    Tempo    │
                                      │  (traces)   │
                                      └─────────────┘
                                             ▲
                                             │
┌─────────────────┐     ┌───────────────────┴──┐     ┌─────────────┐
│   MCP Gateway   │────▶│   OTEL Collector      │────▶│    Loki     │
│ OTLP endpoint   │     │   traces and logs     │     │   (logs)    │
└────────┬────────┘     └──────────────────────┘     └──────┬──────┘
         │                                                    │
         │ /metrics:9090                                     ▼
         │                                             ┌─────────────┐
         │                                             │   Grafana   │
         │                                             │   (3000)    │
         │                                             └──────┬──────┘
         ▼                                                    ▲
┌─────────────────┐                                           │
│   Prometheus    │───────────────────────────────────────────┘
│ (optional, 9090)│
└────────┬────────┘
         ▲
         │ /stats/prometheus:15090
┌────────┴────────┐
│  Istio gateway  │
└─────────────────┘
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

### Generate Traffic with Trace Propagation (sslip.io hostname example)

The following commands intentionally use the local sslip.io gateway hostname
`http://mcp.127-0-0-1.sslip.io:8001/mcp` rather than the local port-forward endpoint.
Use `http://localhost:8001/mcp` instead when you do not need to exercise the hostname.

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

### Generate Authenticated Traffic (AUTH_TRACING=1; sslip.io hostname example)

#### Prerequisites

The Keycloak commands and gateway requests in this section use the local sslip.io
hostnames from the auth example. The equivalent gateway port-forward endpoint is
`http://localhost:8001/mcp`.

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

1. Open http://localhost:3000.
2. Go to **Explore** (compass icon in the left sidebar).
3. Select **Tempo** as the datasource.
4. Click the **Search** tab.
5. Set **Service Name** to `mcp-gateway`.
6. Click **Run query**.
7. Click a trace to see the span waterfall.

### View Logs with Trace Correlation

1. In Grafana, go to **Explore**.
2. Select **Loki** as the datasource.
3. Enter the query `{job="mcp-gateway"}`.
4. Expand a log line and look for `trace_id` and `span_id` fields.
5. Click the `trace_id` value to jump directly to that trace in Tempo.

### View Metrics in Grafana

1. Apply `examples/otel/prometheus.yaml` or configure an external Prometheus with the
   equivalent gateway and Istio scrape targets.
2. Run `make otel-forward`. This target forwards **Grafana only** at
   http://localhost:3000; it does not forward Prometheus or the gateway metrics port.
3. Open http://localhost:3000 and go to **Dashboards → MCP Gateway Metrics**.
4. Select the provisioned **Prometheus** datasource and choose a `server_name` when
   inspecting per-upstream panels.
5. The **Tools Discovered per Server** panel reads `mcp_broker_tools_discovered`;
   see the [OpenTelemetry integration guide](../../docs/guides/opentelemetry.md#broker-metrics)
   for the metric definition and behavior.

To inspect the raw gateway metrics without Grafana, use a separate port-forward:

```bash
kubectl port-forward -n mcp-system deployment/mcp-gateway 9090:9090
curl -s http://localhost:9090/metrics | grep mcp_broker_tools_discovered
```

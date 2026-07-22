# OpenTelemetry Integration

This guide covers enabling OpenTelemetry (OTel) on the MCP Gateway for distributed tracing, log export, and Prometheus metrics. Tracing and log export require an OTLP endpoint to be configured. Prometheus metrics are always enabled and require no configuration.

## Prerequisites

- MCP Gateway installed and configured
- An OTLP-compatible collector endpoint (e.g., [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/), Grafana Alloy, Datadog Agent)

> **Note:** For a pre-configured local stack with OTEL Collector, Tempo, Loki, and Grafana, see the [observability example](https://github.com/Kuadrant/mcp-gateway/tree/release-0.6.0/examples/otel).

## Step 1: Enable OpenTelemetry

Set the following environment variables on the MCP Gateway deployment:

| Variable | Required | Description |
|----------|----------|-------------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Yes | Your OTLP collector endpoint (e.g., `http://your-collector:4318`) |
| `OTEL_EXPORTER_OTLP_INSECURE` | No | Set to `true` for non-TLS endpoints |

### Helm Install

After installing the MCP Gateway with Helm, set the environment variables on the deployment. Use `helm list -A` to find your release name and namespace:

```bash
kubectl set env deployment/<release-name> -n <namespace> \
  OTEL_EXPORTER_OTLP_ENDPOINT="http://your-collector:4318" \
  OTEL_EXPORTER_OTLP_INSECURE="true"
```

### Kubernetes (kubectl)

If you deployed the gateway manifests directly:

```bash
kubectl set env deployment/mcp-gateway -n mcp-system \
  OTEL_EXPORTER_OTLP_ENDPOINT="http://your-collector:4318" \
  OTEL_EXPORTER_OTLP_INSECURE="true"
```

## Step 2: Verify Traces Are Being Exported

After enabling OTel, generate some traffic against the gateway (e.g., an `initialize` or `tools/list` request) and confirm traces appear in your collector backend. The gateway emits spans under the service name `mcp-gateway` by default.

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Base OTLP endpoint for all signals | (none -- disabled) |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | Override endpoint for traces only | Falls back to base |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | Override endpoint for logs only | Falls back to base |
| `OTEL_EXPORTER_OTLP_INSECURE` | Disable TLS verification | `false` |
| `OTEL_SERVICE_NAME` | Service name reported in traces and logs | `mcp-gateway` |
| `OTEL_SERVICE_VERSION` | Service version reported in traces and logs | Build version |

## Endpoint Schemes

The endpoint URL scheme determines the transport protocol:

| Scheme | Protocol | Typical Port | Example |
|--------|----------|-------------|---------|
| `http://` | OTLP/HTTP (insecure) | 4318 | `http://collector:4318` |
| `https://` | OTLP/HTTP (TLS) | 4318 | `https://collector:4318` |
| `rpc://` | OTLP/gRPC | 4317 | `rpc://collector:4317` |

When using `http://`, TLS is automatically disabled regardless of the `OTEL_EXPORTER_OTLP_INSECURE` setting. For `https://` and `rpc://`, set `OTEL_EXPORTER_OTLP_INSECURE=true` to skip TLS verification.

## Sending Traces and Logs to Different Backends

Use signal-specific endpoint overrides to route traces and logs to different collectors or backends:

```bash
kubectl set env deployment/mcp-gateway -n mcp-system \
  OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="http://traces-collector:4318" \
  OTEL_EXPORTER_OTLP_LOGS_ENDPOINT="http://logs-collector:4318" \
  OTEL_EXPORTER_OTLP_INSECURE="true"
```

To enable only traces (without log export), set only `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`. To enable only log export, set only `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`.

## What Gets Exported

### Traces

The MCP Router emits spans for each ext_proc request lifecycle:

```
mcp-router.process
├── mcp-router.route-decision
│   ├── mcp-router.broker-passthrough        (initialize, tools/list, etc.)
│   └── mcp-router.tool-call                 (tools/call)
│       ├── mcp-router.broker.get-server-info
│       ├── mcp-router.session-cache.get
│       ├── mcp-router.session-init          (on cache miss)
│       └── mcp-router.session-cache.store   (on cache miss)
```

Span attributes follow [OpenTelemetry MCP Semantic Conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/mcp/#server) and include:

- `mcp.method.name` -- MCP method (initialize, tools/call, tools/list)
- `gen_ai.operation.name` -- same as `mcp.method.name`
- `gen_ai.tool.name` -- tool name (for tools/call requests)
- `mcp.session.id` -- gateway session ID
- `mcp.server` -- resolved backend server name
- `mcp.route` -- routing decision (`tool-call`, `broker`, or `elicitation-response`)
- `http.method`, `http.path`, `http.request_id`, `http.status_code`
- `jsonrpc.request.id`, `jsonrpc.protocol.version`
- `client.address` -- from x-forwarded-for header

On error, spans include `error.type`, `error_source`, and `http.status_code`.

### Logs

When log export is enabled, all `slog` log lines are sent to the collector via OTLP in addition to stdout. Log lines emitted within a traced request automatically include `trace_id` and `span_id` fields, enabling log-to-trace correlation in backends like Grafana (Loki to Tempo).

### Resource Attributes

Every span and log record includes:

- `service.name` -- from `OTEL_SERVICE_NAME` (default: `mcp-gateway`)
- `service.version` -- from `OTEL_SERVICE_VERSION` or build version
- `vcs.revision` -- git SHA (set at build time)
- `build.go.version` -- Go runtime version

## Trace Context Propagation

The router extracts [W3C Trace Context](https://www.w3.org/TR/trace-context/) (`traceparent` header) from incoming requests. This means:

- If Envoy/Istio is configured with tracing, the router spans automatically join the Istio trace as child spans.
- Clients can pass a `traceparent` header to create end-to-end traces from outside the mesh.
- If no `traceparent` is present, the router creates a new root trace.

Example with explicit trace propagation (replace the URL with your gateway endpoint):

```bash
TRACE_ID=$(openssl rand -hex 16)

curl -s -X POST http://your-gateway-host/mcp \
  -H "Content-Type: application/json" \
  -H "traceparent: 00-${TRACE_ID}-$(openssl rand -hex 8)-01" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}'

echo "Search for trace: $TRACE_ID"
```

## Prometheus Metrics

The broker exposes a Prometheus-compatible `/metrics` endpoint on a dedicated internal port (default `:9090`). This is always enabled — no environment variables or OTLP endpoint required.

### Broker metrics

| Metric | Type | Description |
|--------|------|-------------|
| `mcp_broker_discovery_total` | Counter | Discovery attempts per upstream server, labelled `status=success\|failure` |
| `mcp_broker_discovery_duration_seconds` | Histogram | Duration of `tools/list` calls during discovery |
| `mcp_broker_tools_discovered` | Gauge | Current tool count per upstream server. Set to 0 when a server becomes unreachable |
| `mcp_broker_upstream_connection_failures_total` | Counter | Connection failures per upstream server |
| `mcp_broker_tools_list_response_bytes` | Gauge | Size of the last `tools/list` response per upstream server. Proxy for LLM context overhead |

All metrics use `server_name` as the only label, sourced from the `MCPServerRegistration` name. No high-cardinality labels (session IDs, tool names, call IDs) are used.

### Scraping the metrics endpoint

The metrics port is not routed through the Envoy gateway listener. Scrape it cluster-internally:

```bash
# Port-forward for local inspection (use pod name directly — unready pods are skipped by deployment forward)
POD=$(kubectl get pod -n mcp-system -l app.kubernetes.io/name=mcp-gateway \
  -o jsonpath='{.items[0].metadata.name}')
kubectl port-forward -n mcp-system pod/$POD 9090:9090 &
sleep 1
curl http://localhost:9090/metrics
```

For Prometheus scraping, add the broker pod IP as a scrape target. The broker `Service` is controller-managed and does not expose port 9090, so scrape by pod IP directly or use a `PodMonitor` if you have Prometheus Operator installed.

### Useful PromQL queries

```promql
# Current tool count per upstream server
mcp_broker_tools_discovered

# Discovery failure rate per server
sum(rate(mcp_broker_discovery_total{status="failure"}[5m])) by (server_name)

# Servers with active connection failures
sum(rate(mcp_broker_upstream_connection_failures_total[5m])) by (server_name) > 0

# p99 discovery latency per server
histogram_quantile(0.99, sum(rate(mcp_broker_discovery_duration_seconds_bucket[5m])) by (server_name, le))

# Total tools/list context footprint across all servers
sum(mcp_broker_tools_list_response_bytes)
```

### Istio gateway metrics (built-in)

Istio emits these metrics automatically for all traffic through the gateway — no configuration required:

| Metric | Type | Description |
|--------|------|-------------|
| `istio_requests_total` | Counter | Request count by response code, source, and destination |
| `istio_request_duration_milliseconds` | Histogram | Request latency |

```promql
# 5xx error rate per destination
sum(rate(istio_requests_total{response_code=~"5.."}[5m])) by (destination_service_name)

# 4xx rate per destination (likely misconfiguration)
sum(rate(istio_requests_total{response_code=~"4.."}[5m])) by (destination_service_name)

# p99 request latency per destination
histogram_quantile(0.99, sum(rate(istio_request_duration_milliseconds_bucket[5m])) by (destination_service_name, le))
```

### Istio gateway metrics (optional MCP enrichment)

Istio automatically emits `istio_requests_total` and `istio_request_duration_milliseconds` for all traffic through the gateway. Apply the reference Telemetry resource to add `mcp_server_name` and `mcp_method` labels to those existing metrics:

```bash
kubectl apply -f https://raw.githubusercontent.com/Kuadrant/mcp-gateway/main/examples/otel/istio-mcp-metrics.yaml
```

This promotes the `x-mcp-servername` and `x-mcp-method` headers (already set by the MCP Router) into Prometheus label dimensions without any code changes. After applying:

```promql
# Request rate per MCP server
sum(rate(istio_requests_total[5m])) by (mcp_server_name)

# p99 latency per MCP server
histogram_quantile(0.99, sum(rate(istio_request_duration_milliseconds_bucket[5m])) by (mcp_server_name, le))

# Request breakdown by MCP method
sum(rate(istio_requests_total[5m])) by (mcp_method)
```

> **Cardinality note:** `mcp_tool_name` is available in the reference config but commented out. Each unique tool name adds a label value — enable it only if your tool count is small and bounded.

## Next Steps

- For a pre-configured local observability stack (OTEL Collector, Tempo, Loki, Grafana), see the [observability example](https://github.com/Kuadrant/mcp-gateway/tree/release-0.6.0/examples/otel).
- To scale the gateway with shared session state, see the [scaling guide](./scaling.md).

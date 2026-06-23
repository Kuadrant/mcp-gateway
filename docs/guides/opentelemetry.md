# OpenTelemetry Integration

This guide covers enabling OpenTelemetry (OTel) on the MCP Gateway for distributed tracing, log export, and metrics. When enabled, the MCP Router (ext_proc) emits trace spans for every request, the router and broker export OTLP metrics about the gateway's internal data path, and structured logs can be exported via OTLP. When no endpoint is configured, OTel is completely disabled with zero overhead.

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
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | Override endpoint for metrics only | Falls back to base |
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

## Sending Signals to Different Backends

Use signal-specific endpoint overrides to route traces, logs, and metrics to different collectors or backends:

```bash
kubectl set env deployment/mcp-gateway -n mcp-system \
  OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="http://traces-collector:4318" \
  OTEL_EXPORTER_OTLP_LOGS_ENDPOINT="http://logs-collector:4318" \
  OTEL_EXPORTER_OTLP_METRICS_ENDPOINT="http://metrics-collector:4318" \
  OTEL_EXPORTER_OTLP_INSECURE="true"
```

Each signal is enabled independently by its own endpoint (or the base endpoint). To enable only traces, set only `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`; to enable only metrics, set only `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`; and so on.

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

### Metrics

When a metrics endpoint is configured, the MCP Router and MCP Broker export OTLP metrics on a 30-second interval. These are **operational** metrics about the gateway's internal data path — stream concurrency, session-cache behavior, upstream connection health, and config churn — rather than per-tool-call analytics.

| Metric | Instrument | Unit | Attributes | Description |
|--------|-----------|------|------------|-------------|
| `mcp.router.stream.active` | UpDownCounter | `{stream}` | — | Active ext_proc gRPC streams currently open between Envoy and the router. |
| `mcp.router.session.lookups` | Counter | `{lookup}` | `result` = `hit` \| `miss` | Session-cache lookups labeled by hit or miss (session-cache hit rate). |
| `mcp.router.session.inits` | Counter | `{init}` | — | Backend session initializations (hairpin `initialize` requests) performed on a cache miss. |
| `mcp.session.op.duration` | Histogram | `s` | `op` | Latency of Redis session-store operations. Emitted only when a Redis session store is configured; the in-memory store records nothing. |
| `mcp.broker.upstream.connections` | UpDownCounter | `{connection}` | `mcp.server` | Active connections from the broker to each upstream MCP server. |
| `mcp.broker.upstream.tool_fetch.duration` | Histogram | `s` | `mcp.server` | Time taken to fetch the tool list from each upstream MCP server. |
| `mcp.broker.config.reloads` | Counter | `{reload}` | — | Config reload events processed by the broker. |

Metrics are emitted only when an endpoint resolves (`OTEL_EXPORTER_OTLP_METRICS_ENDPOINT`, falling back to `OTEL_EXPORTER_OTLP_ENDPOINT`). With no endpoint configured, no `MeterProvider` is installed and instrumentation is a no-op.

> **Note:** Metric names use OTLP dotted notation. Exporters that target Prometheus normalize these to underscores (for example, `mcp.router.session.lookups` becomes `mcp_router_session_lookups_total`).

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

## Next Steps

- For a pre-configured local observability stack (OTEL Collector, Tempo, Loki, Grafana), see the [observability example](https://github.com/Kuadrant/mcp-gateway/tree/release-0.6.0/examples/otel).
- To scale the gateway with shared session state, see the [scaling guide](./scaling.md).

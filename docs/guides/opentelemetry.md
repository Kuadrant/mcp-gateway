# OpenTelemetry Integration

This guide covers enabling OpenTelemetry (OTel) on the MCP Gateway for distributed tracing, log export, and Prometheus metrics. Tracing and log export require a base or signal-specific OTLP endpoint. Prometheus metrics are always enabled and require no OTLP endpoint.

## Prerequisites

- MCP Gateway installed and configured
- An OTLP-compatible collector endpoint (for example, [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/), Grafana Alloy, or Datadog Agent) for tracing or log export. Prometheus metrics need no additional infrastructure.

> **Note:** For a pre-configured local stack with OTEL Collector, Tempo, Loki, and Grafana, see the [observability example](https://github.com/Kuadrant/mcp-gateway/tree/main/examples/otel).

## Step 1: Enable OpenTelemetry

Set the following environment variables on the MCP Gateway deployment. The OTLP endpoint variables configure trace and log export only. The `/metrics` endpoint remains available when none of these variables is set.

| Variable | Scope | Description |
|----------|-------|-------------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Traces and logs | Base OTLP collector endpoint, for example `http://your-collector:4318` |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | Traces | Optional trace endpoint override |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | Logs | Optional log endpoint override |
| `OTEL_EXPORTER_OTLP_INSECURE` | Traces and logs | Select an insecure transport when `true` |

### Helm Install

The controller creates the data-plane deployment with the name `mcp-gateway` in the namespace containing the `MCPGatewayExtension` resource. To discover its namespace, query the generated label:

```bash
kubectl get deployment -A -l app.kubernetes.io/name=mcp-gateway \
  -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name'
```

Set the environment variables on that deployment:

```bash
kubectl set env deployment/mcp-gateway -n <namespace> \
  OTEL_EXPORTER_OTLP_ENDPOINT="http://your-collector:4318" \
  OTEL_EXPORTER_OTLP_INSECURE="true"
```

### Kubernetes (kubectl)

If you deployed the gateway manifests directly, use the namespace returned by the discovery command in Step 1:

```bash
kubectl set env deployment/mcp-gateway -n <namespace> \
  OTEL_EXPORTER_OTLP_ENDPOINT="http://your-collector:4318" \
  OTEL_EXPORTER_OTLP_INSECURE="true"
```

## Step 2: Verify Traces Are Being Exported

After enabling OTel, generate traffic against the gateway (for example, an `initialize` or `tools/list` request) and confirm that traces appear in your collector backend. The gateway emits spans under the service name `mcp-gateway` by default.

## Environment Variables

The base endpoint and signal-specific overrides apply to traces and logs. Metrics are exported in Prometheus format independently of these variables.

| Variable | Description | Default |
|----------|-------------|---------|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | Base OTLP endpoint for traces and logs | Unset; traces and logs are disabled unless a signal-specific endpoint is set |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | Override endpoint for traces only | Falls back to base |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` | Override endpoint for logs only | Falls back to base |
| `OTEL_EXPORTER_OTLP_INSECURE` | Select an insecure transport for OTLP trace and log exporters when `true` | `false` |
| `OTEL_SERVICE_NAME` | Service name reported in traces and logs | `mcp-gateway` |
| `OTEL_SERVICE_VERSION` | Service version reported in traces and logs | Build version |

An absent base or signal-specific OTLP endpoint disables trace and log exporters only. Prometheus metrics remain enabled and are served at `/metrics` on port `9090`.

## Endpoint Schemes

The exporter dispatches by endpoint scheme. The supported schemes are:

| Scheme | Protocol | Typical port | Example |
|--------|----------|--------------|---------|
| `http://` | OTLP/HTTP with an insecure transport | 4318 | `http://collector:4318` |
| `https://` | OTLP/HTTP with a secure transport by default | 4318 | `https://collector:4318` |
| `rpc://` | OTLP/gRPC with a secure transport by default | 4317 | `rpc://collector:4317` |

An `http://` endpoint always uses an insecure HTTP transport. For `https://` and `rpc://` endpoints, set `OTEL_EXPORTER_OTLP_INSECURE=true` to select an insecure transport. This setting selects the transport security mode; it does not only disable certificate verification.

## Sending Traces and Logs to Different Backends

Use signal-specific endpoint overrides to route traces and logs to different collectors or backends:

```bash
kubectl set env deployment/mcp-gateway -n <namespace> \
  OTEL_EXPORTER_OTLP_TRACES_ENDPOINT="http://traces-collector:4318" \
  OTEL_EXPORTER_OTLP_LOGS_ENDPOINT="http://logs-collector:4318" \
  OTEL_EXPORTER_OTLP_INSECURE="true"
```

To enable only traces, set `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` without a logs endpoint. To enable only log export, set `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` without a traces endpoint. Prometheus metrics do not use these endpoint variables.

## What Gets Exported

### Traces

The MCP Router emits protocol-aware spans for each external processing (ext_proc) request. The 2025-11-25 protocol is stateful and body-based. The 2026-07-28 protocol is stateless and header-based.

`mcp-router.process` is the root span for one ext_proc stream. It starts when request headers arrive, records request and routing attributes as body phases are processed, records the response status at response headers, and ends after the response headers for a non-streamed response or after the final streamed response-body phase.

#### 2025-11-25 stateful routing

```text
mcp-router.process
└── mcp-router.route-decision
    ├── mcp-router.broker-passthrough
    ├── mcp-router.tool-call
    │   └── mcp-router.session-init (cache miss only)
    ├── mcp-router.prompt-get
    │   └── mcp-router.session-init (cache miss only)
    ├── mcp-router.resource-read
    │   └── mcp-router.session-init (cache miss only)
    └── mcp-router.elicitation-response
```

The stateful router uses `mcp-router.resource-read` for `resources/read`, `mcp-router.elicitation-response` for elicitation responses, and `mcp-router.session-init` when a routed request needs a backend session that is not already available. Session initialization hairpins an `initialize` request through the gateway and stores the resulting backend session for later requests.

#### 2026-07-28 stateless routing

```text
mcp-router.process
└── mcp-router.route-decision
    ├── mcp-router.broker-passthrough
    ├── mcp-router.tool-call
    └── mcp-router.prompt-get
```

The stateless router routes tools and prompts with request headers. It does not emit `mcp-router.session-init` or any cache-specific span, and it does not hairpin an initialization request.

Current tool and prompt routing performs routing-table lookups inside the `mcp-router.tool-call` or `mcp-router.prompt-get` span. Tool calls use `LookupTool` and fall back to `LookupPrefix`; prompt calls use `LookupPrompt`. The stateful resource route uses `LookupResourcePrefix`. These lookups do not emit separate spans.

#### Router span attributes

Attributes follow [OpenTelemetry MCP Semantic Conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/mcp/#server). Attributes below are recorded only on the listed span and when the source value exists. All router spans carry `component=mcp-router`.

| Span or protocol | Attributes |
|------------------|------------|
| `mcp-router.process` | Request-header attributes `http.method`, `http.path`, `http.request_id`, `mcp.protocol_version`, `mcp.header.method`, and `mcp.header.name`; parsed-request attributes `mcp.method.name`, `jsonrpc.protocol.version`, `jsonrpc.request.id`, `gen_ai.operation.name`, `gen_ai.tool.name` for tool calls, `mcp.session.id` when a stateful session is present, and `client.address` when `x-forwarded-for` is present; selected router `mcp.router` (`202511` or `202607`); response attributes `http.status_code` and `mcp.response.protocol_version` |
| `mcp-router.route-decision` | `mcp.method.name`, `protocol.version`, and `mcp.route`. The 2025-11-25 route can be `tool-call`, `prompt-get`, `resource-read`, `elicitation-response`, or `broker`; the 2026-07-28 route can be `tool-call`, `prompt-get`, or `broker` |
| 2025-11-25 `mcp-router.tool-call` | `gen_ai.tool.name`, `mcp.session.id`, `mcp.server`, and `mcp.server.hostname` |
| 2025-11-25 `mcp-router.prompt-get` | `mcp.prompt.name`, `mcp.session.id`, `mcp.server`, and `mcp.server.hostname` |
| 2025-11-25 `mcp-router.resource-read` | `mcp.resource.uri`, `mcp.session.id`, `mcp.server`, and `mcp.server.hostname` |
| 2025-11-25 `mcp-router.session-init` | `mcp.server` and `mcp.session.id` |
| 2025-11-25 `mcp-router.elicitation-response` | `mcp.session.id` |
| 2026-07-28 `mcp-router.tool-call` | `gen_ai.tool.name`, `mcp.server`, and `mcp.server.hostname`; this stateless span does not set `mcp.session.id` |
| 2026-07-28 `mcp-router.prompt-get` | `mcp.prompt.name`, `mcp.server`, and `mcp.server.hostname`; this stateless span does not set `mcp.session.id` |
| `mcp-router.broker-passthrough` | `mcp.method.name` |

#### Broker spans

The MCP Broker emits spans for request handling, capability filtering, resource filtering, and upstream management:

| Span | When | Attributes |
|------|------|------------|
| `mcp-broker.handle-request` | Every MCP request handled by the broker | `mcp.method` and `protocol.version`; `mcp.session.id` when the SDK request has a stateful session |
| `mcp-broker.tools-list` | `tools/list` response filtering | `protocol.version` and `mcp.tools.count` |
| `mcp-broker.prompts-list` | `prompts/list` response filtering | `protocol.version` and `mcp.prompts.count` |
| `mcp-broker.resources-list` | `resources/list` response filtering | `mcp.resources.count` |
| `mcp-broker.upstream-manage` | Upstream lifecycle work | `mcp.server`; runs at startup, on the health-check timer, after list-changed notifications, and after reconnect events |

The router propagates W3C trace context on forwarded requests. The broker extracts that context, so router and broker work can be correlated in one trace. Broker filtering spans are children of `mcp-broker.handle-request`, not direct children of router route spans.

#### Error attributes

Error recording is component-specific. An error span does not automatically contain every error attribute:

- ext_proc processing errors record `error.type`, `error_source=ext-proc`, and `http.status_code`.
- Broker handler errors record `error.type` and `error_source=broker`.
- Backend connection or discovery errors recorded by `mcp-broker.upstream-manage` record `error.type`, `error_source=backend`, and `mcp.server`.
- Router route errors can set span status and `error.type`. The resulting HTTP status is recorded on `mcp-router.process` when response headers are processed.

### Logs

When log export is enabled, all `slog` log lines are sent to the collector through OTLP in addition to stdout. Log lines emitted within a traced request automatically include `trace_id` and `span_id` fields, enabling log-to-trace correlation in backends such as Grafana (Loki to Tempo).

### Resource Attributes

Enabled trace spans and log records can include:

- `service.name` from `OTEL_SERVICE_NAME` (default: `mcp-gateway`)
- `service.version` from `OTEL_SERVICE_VERSION` or the build version
- `vcs.revision` when a git SHA is set at build time
- `vcs.dirty` when the build provides a dirty-state value
- `build.go.version` for the Go runtime version

## Trace Context Propagation

The router extracts [W3C Trace Context](https://www.w3.org/TR/trace-context/) (`traceparent` header) from incoming requests. When Envoy or Istio tracing is configured, router spans join the existing trace. Clients can pass a `traceparent` header to create end-to-end traces from outside the mesh. If no `traceparent` is present, the router creates a new root trace.

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

The broker exposes a Prometheus-compatible `/metrics` endpoint on a dedicated pod-local port (default `:9090`). Metrics are always enabled and do not require environment variables or an OTLP endpoint.

### Broker metrics

| Metric | Type | Description |
|--------|------|-------------|
| `mcp_broker_discovery_total` | Counter | Discovery attempts per upstream server, labeled `status=success` or `status=failure` |
| `mcp_broker_discovery_duration_seconds` | Histogram | Duration of `tools/list` calls during discovery |
| `mcp_broker_tools_discovered` | Gauge | Cached tool count per upstream server. Cached values survive transient connection or ping failures and reach zero after three consecutive connection or ping failures |
| `mcp_broker_upstream_connection_failures_total` | Counter | Connection failures per upstream server |
| `mcp_broker_tools_list_response_bytes` | Gauge | Serialized size of the validated tool list per upstream server. Proxy for LLM context overhead |

`mcp_broker_discovery_total` uses `server_name` and `status` labels. All other metrics use only `server_name`. Label values are formatted as `namespace/name`, matching the namespace and name of the `MCPServerRegistration` resource (for example, `<namespace>/my-server`). No high-cardinality labels (session IDs, tool names, or call IDs) are used.

### Scraping the metrics endpoint

The broker metrics endpoint is not routed through the Envoy gateway listener. The generated broker `Service` exposes the HTTP and gRPC ports but not port `9090`, so scrape the pod-local endpoint directly:

```bash
# Replace <namespace> with the gateway namespace from Step 1.
POD=$(kubectl get pod -n <namespace> -l app.kubernetes.io/name=mcp-gateway \
  -o jsonpath='{.items[0].metadata.name}')
kubectl port-forward -n <namespace> pod/$POD 9090:9090 &
sleep 1
curl http://localhost:9090/metrics
```

For Prometheus scraping, replace `<namespace>` below with the gateway namespace from Step 1. Use Kubernetes pod service discovery and replace each pod IP with port `9090`:

```yaml
scrape_configs:
  - job_name: mcp-broker
    kubernetes_sd_configs:
      - role: pod
        namespaces:
          names: ["<namespace>"]
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_app_kubernetes_io_name]
        action: keep
        regex: mcp-gateway
      - source_labels: [__meta_kubernetes_pod_ip]
        target_label: __address__
        replacement: $1:9090
```

This guide assumes that Prometheus is already installed. Add the scrape job above to its configuration.

If you use Prometheus Operator, configure an equivalent pod-IP scrape job in the operator-managed Prometheus instance. The generated broker container does not declare a named `metrics` port, and the generated `Service` does not expose port `9090`, so a `PodMonitor` with `port: metrics` does not work for the default deployment unless you add that named port.

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

Istio emits these Envoy metrics automatically for traffic through the gateway. The Istio gateway pod exposes them at `:15090/stats/prometheus`; they are separate from the broker metrics served by the pod-local `:9090` endpoint. Add a scrape job for these metrics alongside the broker scrape config:

> **Note:** If you are using the Kuadrant Operator, observability configuration (including Prometheus scraping) is managed centrally via the `Kuadrant` CR. See the [Kuadrant observability guide](https://docs.kuadrant.io/latest/kuadrant-operator/doc/observability/) for details.

```yaml
  - job_name: istio-gateway
    metrics_path: /stats/prometheus
    kubernetes_sd_configs:
      - role: pod
        namespaces:
          names: ["<gateway-namespace>"]
    relabel_configs:
      - source_labels: [__meta_kubernetes_pod_label_gateway_istio_io_managed]
        action: keep
        regex: .+
      - source_labels: [__meta_kubernetes_pod_ip]
        target_label: __address__
        replacement: $1:15090
```

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

Apply the following Telemetry resource in the namespace that contains the Istio Gateway. Replace `<gateway-namespace>` before running the command:

```bash
kubectl apply -f - <<EOF
apiVersion: telemetry.istio.io/v1
kind: Telemetry
metadata:
  name: mcp-metrics
  namespace: <gateway-namespace>
spec:
  metrics:
    - providers:
        - name: prometheus
      overrides:
        - match:
            metric: REQUEST_COUNT
          tagOverrides:
            mcp_server_name:
              operation: UPSERT
              value: "request.headers['x-mcp-servername']"
            mcp_method:
              operation: UPSERT
              value: "request.headers['x-mcp-method']"
            # mcp_tool_name adds a label per unique tool. Enable only when the tool count is bounded.
            # mcp_tool_name:
            #   operation: UPSERT
            #   value: "request.headers['x-mcp-toolname']"
        - match:
            metric: REQUEST_DURATION
          tagOverrides:
            mcp_server_name:
              operation: UPSERT
              value: "request.headers['x-mcp-servername']"
            mcp_method:
              operation: UPSERT
              value: "request.headers['x-mcp-method']"
            # mcp_tool_name:
            #   operation: UPSERT
            #   value: "request.headers['x-mcp-toolname']"
EOF
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

- For a pre-configured local observability stack (OTEL Collector, Tempo, Loki, Grafana), see the [observability example](https://github.com/Kuadrant/mcp-gateway/tree/main/examples/otel).
- To scale the gateway with shared session state, see the [scaling guide](./scaling.md).

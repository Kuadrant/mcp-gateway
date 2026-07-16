# Metrics: Observability Phase 3

## Problem

The MCP Gateway has distributed tracing (Phase 1-2) but no metrics. Operators deploying the gateway cannot answer basic operational questions:

- Are my upstream MCP servers reachable and serving tools?
- How fast is discovery, and are reconnections happening?
- How much context (tool metadata) is the gateway contributing to conversations?

The `discoveredTools` status field on `MCPServerRegistration` was [removed](https://github.com/Kuadrant/mcp-gateway/pull/1187) as part of eliminating controller-to-broker status polling. This left a gap: operators have no visibility into what the broker has discovered from upstream servers.

Gateway-level HTTP metrics exist via Istio (`istio_requests_total`, `istio_request_duration_milliseconds`) but they operate at the HTTP layer and cannot see MCP-level semantics like tool names, server names, or discovery outcomes.

## Summary

Add OpenTelemetry metrics to the broker for discovery health, tool counts, and response sizes. Document existing gateway metrics for request rate and latency. Provide a reference Istio Telemetry configuration that adds MCP dimensions (tool name, server name) to existing gateway metrics via header-based tag overrides.

## Goals

- Give operators visibility into upstream server health and discovery outcomes
- Replace the removed `discoveredTools` status field with a runtime metric
- Surface tool list response sizes as a proxy for context contribution
- Document what's already available from gateway metrics
- Provide a reference Istio config for MCP-aware gateway metrics

## Non-Goals

- Router (ext_proc) metrics: deferred to a future phase
- MCP-level error detection (parsing response bodies for `isError`): out of scope
- Grafana dashboards: deferred (metrics come first, dashboards follow)
- Custom Envoy filter metrics: the gateway implementation will change, so investment here is limited to Istio Telemetry configuration

## Target Persona

The gateway operator: the person deploying the MCP Gateway, creating `MCPServerRegistration` resources, and wiring upstream MCP servers. They need to know if their configuration is working and how traffic is flowing.

## Metrics Sources

Three components can surface metrics, each covering a different layer:

| Source | What it sees | This phase |
|--------|-------------|------------|
| Gateway (Envoy/Istio) | HTTP request rate, status codes, latency per route | Document existing metrics. Reference Istio `tagOverrides` config for MCP dimensions. |
| Broker | Discovery outcomes, tool counts, upstream reachability, federated response sizes | New OTel metrics. |
| Router (ext_proc) | MCP method, tool name, server name per request | Deferred. Gateway metrics cover request rate and latency. |

## Job Stories

### When I register an upstream MCP server and want to know it's working

When a gateway operator creates an `MCPServerRegistration` and the broker attempts discovery, they want to see whether discovery succeeded and how many tools were found, so that they can confirm the server is correctly wired and accessible.

### When upstream servers become unreachable

When an upstream MCP server goes down or starts failing discovery, the operator wants to see discovery failures and reconnection attempts in metrics, so that they can be alerted and investigate before clients are impacted.

### When tool calls are slow and the operator needs to identify the bottleneck

When a gateway operator receives reports of slow tool calls, they want to see per-route and (optionally) per-server latency, so that they can identify which upstream server is the bottleneck. Gateway metrics provide per-route latency. Istio tag overrides can add per-server granularity using headers the router already sets.

### When the operator wants to understand context contribution

When a gateway operator wants to understand how much tool metadata the gateway is contributing to conversations across all federated servers, they want to see tool list response sizes and tool counts per server, so that they can identify servers contributing disproportionate context and make informed decisions about which servers to expose, filter, or prioritize for output compression.

Context bloat from tool metadata is a common problem for any MCP client pulling tools from multiple servers. The gateway is the natural aggregation point, providing centralized visibility across all servers.

### When the operator needs to confirm gateway infrastructure is healthy

When a gateway operator wants to confirm that the gateway, router, and broker are running and processing requests, they want to use existing Kubernetes probes and gateway metrics (`istio_requests_total`), so that they can monitor infrastructure health without additional instrumentation.

## Design

### Gateway Metrics (existing)

These metrics already exist via Istio and require no new code:

- `istio_requests_total` — request count by response code, reporter, source, destination
- `istio_request_duration_milliseconds` — request latency histogram

These cover request rate, latency, and HTTP-level error rates per route. No new instrumentation needed, but they should be documented for operators.

```promql
# 5xx error rate per destination
sum(rate(istio_requests_total{response_code=~"5.."}[5m])) by (destination_service_name)

# 4xx rate per destination (likely misconfiguration)
sum(rate(istio_requests_total{response_code=~"4.."}[5m])) by (destination_service_name)

# p99 request latency per destination
histogram_quantile(0.99, sum(rate(istio_request_duration_milliseconds_bucket[5m])) by (destination_service_name, le))
```

### Istio Telemetry Tag Overrides (optional)

The router already sets `x-mcp-servername`, `x-mcp-toolname`, and `x-mcp-method` as request headers. Istio's Telemetry API can promote these to metric labels without code changes:

```yaml
apiVersion: telemetry.istio.io/v1
kind: Telemetry
metadata:
  name: mcp-metrics
  namespace: gateway-system
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
        # optional: adds a label per unique tool, which can increase cardinality significantly
        mcp_tool_name:
          operation: UPSERT
          value: "request.headers['x-mcp-toolname']"
        mcp_method:
          operation: UPSERT
          value: "request.headers['x-mcp-method']"
    - match:
        metric: REQUEST_DURATION
      tagOverrides:
        mcp_server_name:
          operation: UPSERT
          value: "request.headers['x-mcp-servername']"
        # optional: adds a label per unique tool, which can increase cardinality significantly
        mcp_tool_name:
          operation: UPSERT
          value: "request.headers['x-mcp-toolname']"
```

A reference configuration will be provided in `examples/otel/istio-mcp-metrics.yaml`. Cardinality scales with the number of unique tools, which is operator-controlled.

```promql
# request rate per MCP server
sum(rate(istio_requests_total[5m])) by (mcp_server_name)

# top 10 most called tools (requires mcp_tool_name tag override, watch cardinality)
topk(10, sum(rate(istio_requests_total[5m])) by (mcp_tool_name))

# p99 tool call latency per MCP server
histogram_quantile(0.99, sum(rate(istio_request_duration_milliseconds_bucket[5m])) by (mcp_server_name, le))
```

### Broker Metrics

The broker is the only component that sees discovery outcomes and upstream reachability. All metrics use the OpenTelemetry metrics API, consistent with existing OTel tracing in the broker.

#### Discovery health

```
mcp_broker_discovery_total
  type: counter
  labels: server_name, status (success | failure)
  description: number of discovery attempts per upstream server
```

```promql
# discovery failure rate per server
sum(rate(mcp_broker_discovery_total{status="failure"}[5m])) by (server_name)

# failure ratio per server
sum(mcp_broker_discovery_total{status="failure"}) by (server_name) / sum(mcp_broker_discovery_total) by (server_name)
```

```
mcp_broker_discovery_duration_seconds
  type: histogram
  labels: server_name
  description: time taken for tools/list calls during discovery
```

```promql
# p99 discovery latency per server
histogram_quantile(0.99, sum(rate(mcp_broker_discovery_duration_seconds_bucket[5m])) by (server_name, le))
```

#### Tool inventory

```
mcp_broker_tools_discovered
  type: gauge
  labels: server_name
  description: current number of tools discovered per upstream server.
               replaces the removed discoveredTools status field.
               set to 0 when a server becomes unreachable.
```

```promql
# current tool count per server
sum(mcp_broker_tools_discovered) by (server_name)
```

#### Upstream stability

```
mcp_broker_upstream_connection_failures_total
  type: counter
  labels: server_name
  description: number of upstream connection failures. incremented each time
               manage() enters handleConnectionFailure, covering both failed
               Connect() and failed Ping() on an existing connection.
               frequency decays during sustained outages due to exponential
               backoff on the health check ticker.
```

```promql
# servers currently experiencing connection failures
sum(rate(mcp_broker_upstream_connection_failures_total[5m])) by (server_name) > 0
```

#### Context contribution

```
mcp_broker_tools_list_response_bytes
  type: gauge
  labels: server_name
  description: last observed size in bytes of tools/list response per upstream server.
               updates on each discovery. set to 0 when a server becomes
               unreachable. removed when the server registration is deleted.
               sum across servers gives total gateway context footprint.
```

```promql
# total tools/list payload across all servers
sum(mcp_broker_tools_list_response_bytes)
```

#### Cardinality

`server_name` is bounded by the number of `MCPServerRegistration` resources, which is operator-controlled and typically in the tens, not thousands. No high-cardinality labels (session IDs, tool call IDs) are used. These belong in traces and logs.

### Metrics Endpoint

The broker exposes a Prometheus-compatible `/metrics` endpoint via the OTel Prometheus exporter. This is consistent with standard Kubernetes metrics scraping patterns and does not require an OTel Collector for basic usage.

### Prerequisites

- OTel SDK already integrated for tracing
- Prometheus exporter dependency added to the broker

## Security Considerations

- No sensitive data in metric labels: no session IDs, tokens, or credentials
- `server_name` labels use the operator-defined name from `MCPServerRegistration`, not upstream hostnames or internal identifiers
- Metrics endpoint should be accessible only within the cluster (not exposed through the gateway)

## Known Limitations

### MCP-level tool errors are not visible in metrics

A tool call returning HTTP 200 with `isError: true` in the JSON-RPC result is invisible to both gateway and broker metrics. Detecting this would require parsing every tool call response body in the router, which is not justified for this phase. This is an application-level concern between the MCP client and the upstream server, not a gateway operator concern.

### Router metrics deferred

Adding metrics to the router would provide native MCP-aware request rate and latency without depending on Istio tag overrides. Deferred to keep scope small (see Metrics Sources table).

### No dashboards

This phase delivers metrics, not dashboards. Grafana dashboard definitions are a natural follow-up once the metrics schema is validated in practice.

## Future Considerations

- Router metrics for native MCP-aware request rate and latency
- Grafana dashboard definitions
- Tool response size metrics (requires response body access in the router)
- Per-tool schema size metrics for identifying individual outlier tool definitions within a server
- Alerting rule templates (e.g., discovery failure rate > threshold)

## Execution

Implementation plan to follow after design approval.

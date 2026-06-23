## Observability

### Problem

The MCP Gateway consists of multiple components working together to route and broker MCP protocol requests: Envoy (gateway), MCP Router (ext_proc), MCP Broker, and backend MCP Servers. When issues occur, such as 404 errors, failed tool calls or unexpected responses when tools are used by agents, operators and developers need visibility into:

- Which component generated the error (Envoy, MCP Router, MCP Broker, or backend MCP Server)
- How requests flow through the system components
- What tools were called & which MCP servers executed them
- Where to find relevant logs for each component
- Metrics about tool call patterns, success rates, and error rates
- Distributed tracing across the request path

Without comprehensive observability, debugging issues becomes difficult and time-consuming, especially when trying to determine if a 404 response originated from the gateway, the router, the broker, or the backend MCP server.

### Solution

The MCP Gateway leverages [OpenTelemetry](https://opentelemetry.io/) throughout all components to provide consistent logging, distributed tracing, and metrics. This enables:

- **Distributed Tracing**: End-to-end request tracing across Envoy, MCP Router, MCP Broker, and backend MCP Servers
- **Structured Logging**: Consistent log formats with correlation IDs to link logs across components
- **Metrics**: Tool call counts, rates, and error rates by tool name and backend MCP server
- **Error Attribution**: Clear identification of which component generated errors

All components instrument their operations using OpenTelemetry, ensuring consistent observability data that can be collected and exported to various backends (e.g., Jaeger, Prometheus, Grafana, etc.).

### Current Implementation Status

Tracing and structured log export are implemented across the router and broker (see the [OpenTelemetry guide](../guides/opentelemetry.md)). Metrics export was added incrementally and currently covers an **operational** slice of the data path — instrumentation that helps operators reason about gateway health and capacity — rather than the per-tool-call analytics described in Journey 2 below.

Implemented metrics (exported via OTLP when a metrics endpoint is configured):

| Metric | Instrument | Component | Purpose |
|--------|-----------|-----------|---------|
| `mcp.router.stream.active` | UpDownCounter | router | active ext_proc gRPC streams |
| `mcp.router.session.lookups` (`result=hit\|miss`) | Counter | router | session-cache hit/miss rate |
| `mcp.router.session.inits` | Counter | router | backend session initializations on cache miss |
| `mcp.session.op.duration` | Histogram | session | Redis session-store operation latency |
| `mcp.broker.upstream.connections` (`mcp.server`) | UpDownCounter | broker | active upstream connections |
| `mcp.broker.upstream.tool_fetch.duration` (`mcp.server`) | Histogram | broker | upstream tool-fetch latency |
| `mcp.broker.config.reloads` | Counter | broker | config reload events |

Still planned (not yet implemented):

- Tool-call analytics metrics (`mcp_tool_calls_total`, `mcp_tool_call_duration_seconds`, `mcp_requests_total` by tool/server/status) — see Journey 2.
- Component-level 404/error attribution metrics — see Journey 1.
- Tool Call Graph signals — see Journey 3.

The metric names in the journeys below are illustrative targets, not the names of the currently exported instruments. The user-facing reference for the implemented metrics lives in the [OpenTelemetry guide](../guides/opentelemetry.md).

### User Journeys

#### Journey 1: Debugging 404 Errors ("What Went Wrong")

**As a MCP Server developer or MCP Gateway admin**, I make a tool call or become aware of failing tool calls returning 404s. This is a **"what went wrong"** scenario - I need to identify the failure point and understand the technical error. I need to determine:

- Is this response from the MCP Gateway, or from the MCP server?
- Did something go wrong with the `/mcp` endpoint, or the internal routing after the ext_proc?
- Where can I see these 404s (count, frequency)?
- How do I trace a specific 404 through the different components (Envoy, MCP Router/ext_proc, MCP Broker if applicable, MCP Server)?
- What logs exist for that 404 in each component?

**Solution Requirements:**

- Distributed trace showing the full request path with spans for each component
- Error attribution clearly identifying which component returned the 404
- Log correlation using trace IDs to find all logs related to a specific request
- Metrics dashboard showing 404 counts by component, tool name, and backend MCP server
- Component-specific logs documented

**Trace Flow:**
```
Client Request
  └─> Envoy (gateway entry point)
      └─> MCP Router (ext_proc - routing decision)
          └─> MCP Broker (if applicable)
          └─> Backend MCP Server (tool execution)
```

Each component should emit:
- OpenTelemetry spans with appropriate attributes (tool name, MCP server name, HTTP status, etc.)
- Structured logs with trace ID correlation
- Error details indicating the source of 404s

The trace flow will be more complex when statefulness comes into play with elicitations. This will need some thought if and how an elicitation response in a separate follow up message from the original tool call can be tied easily together.

#### Journey 2: Tool Call Analytics

**As an MCP Gateway admin**, I want to see a breakdown of all tool calls going through the gateway:

- Count/rate of requests for each tool name
- Count/rate of requests per backend MCP server name
- Success vs. error rates
- Latency percentiles
- Tool call patterns over time

**Solution Requirements:**

> **Note:** The tool-call metrics in this journey are **not yet implemented**. The current metrics export covers operational signals only (see Current Implementation Status above). The names below are proposed targets.

- Metrics exported via OpenTelemetry metrics API
- Key metrics:
  - `mcp_tool_calls_total` - Counter with labels: `tool_name`, `mcp_server_name`, `status_code`
  - `mcp_tool_call_duration_seconds` - Histogram with labels: `tool_name`, `mcp_server_name`
  - `mcp_requests_total` - Counter with labels: `method`, `status_code`, `component`
- Dashboard visualization (e.g., Grafana)
- Ability to filter and aggregate by tool name, MCP server, time range

**Metrics Cardinality Considerations**

High cardinality fields like session ids are best left to traces and logs.
A tool call id, like `mcp.tool_call_id = 12345` can be sufficient.
If the number of tools is thousands, a label may not be the best idea.

#### Journey 3: Understanding Tool Call Graphs ("Why Did That Happen")

**As an agent or model using the MCP Gateway**, or **as an MCP Gateway admin supporting agent/model users**, I need to understand **"why did that happen"** when:

- A tool call sequence didn't produce the expected result
- The agent/model made unexpected tool call decisions
- I need to understand the reasoning chain that led to a particular outcome
- I need to debug why an answer or result wasn't as expected

This is distinct from "what went wrong" (Journey 1) - here we're not looking for technical errors, but rather understanding the **logical flow and context** of tool calls made by an agent or model. This problem cannot be solved entirely within the MCP Gateway, however we should aim to enable as much as possible via standard mechanisms. To that end, this user journey may be satisfied in time by more focused journeys as observability requirements become clearer from outside the MCP Gateway. 

**Tool Call Graphs:**

A **Tool Call Graph** represents the sequence and relationships of tool calls made during an agent/model interaction. It shows:

- The chronological sequence of tool calls
- Which tools were called and in what order
- The inputs and outputs of each tool call
- Dependencies between tool calls (e.g., tool B was called with output from tool A)
- The context and reasoning that led to each tool call
- The final outcome or answer produced

**Solution Requirements:**

To enable Tool Call Graph construction and analysis, the gateway must surface observability signals that capture:

1. **Tool Call Sequences**: 
   - Trace IDs that link related tool calls within a session or conversation
   - Session/conversation identifiers to group related tool calls
   - Timestamps for chronological ordering

2. **Tool Call Context**:
   - Tool name and parameters for each call
   - Tool call results/outputs
   - Which backend MCP server handled each tool call
   - Any elicitations or user interactions that occurred

3. **Relationships**:
   - Links between tool calls (e.g., tool B used output from tool A)
      - For example: `mcp.session_id: "sess-1234"`, `mcp.tool_call_id: "tc-2"`, `mcp.tool_call_parent_id: "tc-1"` (with session & parent optional)
   - Dependencies and data flow between tool calls
   - Branching or conditional tool call paths

**Observability Signals Needed:**

- **Distributed Traces**: End-to-end traces that span multiple tool calls within a session, with spans for each tool call showing inputs, outputs, and timing
- **Structured Logs**: Logs that include session IDs, conversation IDs, tool call sequences, and context
- **Metrics**: Tool call patterns, sequences, and relationships
- **Event Streams**: Real-time or historical event streams showing tool call sequences

**Implementation Notes:**
See the "Tool Call Graphs" section under Implementation Considerations for implementation details.

### Component Observability Requirements

#### Envoy Gateway

- Access logs with MCP-specific context (tool name, MCP server destination)
- OpenTelemetry tracing integration
- Metrics for request rates, error rates, latency
- Correlation of Envoy logs with downstream component traces

#### MCP Router (ext_proc)

- Structured logs for routing decisions
- OpenTelemetry spans for request processing
- Logs for 404 detection and session invalidation
- Metrics for routing decisions (broker vs. backend server)

#### MCP Broker

- Structured logs for broker operations (init, tools/list aggregation, notifications)
- OpenTelemetry spans for broker-to-backend-server calls
- Metrics for aggregated operations
- Logs for notification brokering

#### MCP Discovery Controller

- Structured logs for resource reconciliation
- Metrics for discovered MCP servers
- Status reporting and health metrics

### Implementation Considerations

Implementation will leverage OpenTelemetry for consistent instrumentation across all components. Key areas to address include:

- **OpenTelemetry Integration**: SDK selection, OTLP exporter configuration, trace context propagation (W3C Trace Context), resource attributes
- **Sampling strategies**:
    - Sampling should be configurable, where 1.0 is all requests traced and 0.0 is none. 
    - Determine whether or not to sample in the entry span and propagate from there
    - When we have an error we should **always** sample (could propagate this with a label like: `mcp.error=true`)
    - We could also add a debug label for temporarily forcing a sample in a prod/staging system via `mcp.debug=true`.
- **Logging**: Structured log formats with trace correlation, standardized log levels, and MCP-specific context fields
- **Metrics**: Metric naming conventions, OpenTelemetry metrics API usage, and aggregation configuration
- **Distributed Tracing**: Trace context propagation through Envoy and ext_proc, span attributes, and elicitation response correlation
- **Error Attribution**: Error attributes in spans, HTTP status code inclusion, component tagging, and clear error source identification
- **Tool Call Analytics**: Metrics schema definition and collection implementation
- **Tool Call Graphs**: Session/conversation ID propagation, tool call correlation, input/output capture with sanitization, relationship tracking, elicitation event surfacing, event streams, and data model definition

### Security Considerations

Observability data must be handled securely:

- Sensitive data (tokens, client credentials, mcp server credentials for the broker) should not be included in traces/logs
- Tool call parameters should be sanitized in observability data

### Open Questions

1. **Performance Impact**: What is the acceptable performance overhead for observability instrumentation? Should observability be configurable/optional? Having a per component toggle would be desirable, with different verbosity levels.

2. **Kiali**: How does Kiali interact, and what opportunities are there for integration?

TODO: Implementation Details

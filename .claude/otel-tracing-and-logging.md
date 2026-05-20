# OpenTelemetry Tracing and Logging

## Current Setup

OTel is configured via environment variables. When no `OTEL_EXPORTER_OTLP_ENDPOINT` is set,
OTel is fully disabled (zero overhead). The setup lives in `internal/otel/`:

- `config.go` -- reads env vars, determines which signals are enabled
- `otel.go` -- `SetupOTelSDK()` wires up propagator, tracer provider, logger provider, and shutdown
- `provider.go` -- creates trace exporter (HTTP or gRPC based on URL scheme)
- `logs.go` -- creates log exporter (same scheme dispatch)
- `logging.go` -- `TracingHandler` injects `trace_id`/`span_id` into slog records; `MultiHandler` fans out to stdout + OTLP
- `resource.go` -- shared resource with `service.name`, `service.version`, `vcs.revision`

The global tracer provider is set in `cmd/mcp-broker-router/main.go` via `otel.SetTracerProvider()`.
The logger is created with `NewTracingLogger()` which wraps slog with trace context injection.

## Tracing Conventions

### Span Naming
Spans are prefixed by component:

**Router** (`mcp-router.`):
- `mcp-router.process` -- root span for the full ext_proc stream lifecycle
- `mcp-router.route-decision` -- routing decision (tool-call, prompt-get, elicitation-response, or broker)
- `mcp-router.tool-call` -- full tool call handling
- `mcp-router.prompt-get` -- full prompt get handling
- `mcp-router.elicitation-response` -- elicitation response routing
- `mcp-router.broker-passthrough` -- pass-through to broker
- `mcp-router.broker.get-server-info` -- broker lookup for tool server info
- `mcp-router.broker.get-server-info-by-prompt` -- broker lookup for prompt server info
- `mcp-router.session-cache.get` -- session cache read
- `mcp-router.session-cache.store` -- session cache write
- `mcp-router.session-init` -- hairpin initialize to backend

**Broker** (`mcp-broker.`):
- `mcp-broker.handle-request` -- wraps every MCP request to the broker
- `mcp-broker.tools-list` -- tools/list filtering (authorization, virtual server, gateway metadata)
- `mcp-broker.prompts-list` -- prompts/list filtering
- `mcp-broker.upstream-manage` -- periodic backend health check and tool/prompt discovery

When adding new spans, follow the naming pattern: `<component>.<action>`.

### Tracer Names
Tracer names are defined as constants:
- `mcpotel.BrokerTracerName` (`internal/otel/otel.go`) -- used by broker and upstream packages
- `tracerName` (`internal/mcp-router/tracing.go`) -- router-local constant


### Span Attributes
Follow [OpenTelemetry MCP Semantic Conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/mcp/#server):

- `mcp.method.name` -- MCP method (initialize, tools/call, tools/list, prompts/get, prompts/list)
- `mcp.method` -- MCP method (broker spans use this shorter form)
- `mcp.route` -- routing decision: `tool-call`, `prompt-get`, `elicitation-response`, or `broker`
- `gen_ai.tool.name` -- tool name
- `gen_ai.operation.name` -- same as mcp.method.name
- `mcp.session.id` -- gateway session ID
- `mcp.server` -- backend server name
- `mcp.server.hostname` -- backend server hostname
- `mcp.prompt.name` -- prompt name (prompts/get)
- `mcp.tools.count` -- number of tools after filtering (broker tools-list span)
- `mcp.prompts.count` -- number of prompts after filtering (broker prompts-list span)
- `jsonrpc.request.id` -- JSON-RPC request ID
- `jsonrpc.protocol.version` -- always "2.0"
- `http.method`, `http.path`, `http.request_id`, `http.status_code`
- `client.address` -- from x-forwarded-for

For new attributes, check OTel semantic conventions first before inventing custom ones.

### Error Recording
Use `mcpotel.SpanError()` (`internal/otel/otel.go`) for the common `RecordError` + `SetStatus` pair:

```go
mcpotel.SpanError(span, err, "description of what failed")
```

The component-specific wrappers add extra attributes on top of `SpanError`:
- `recordError(span, err, statusCode)` in `internal/mcp-router/tracing.go` -- adds `error_source=ext-proc` and `http.status_code`
- `recordBrokerError(span, err)` in `internal/broker/tracing.go` -- adds `error_source=broker`
- `recordBackendError(span, err)` in `internal/broker/upstream/manager.go` -- adds `error_source=backend` and `mcp.server`

Use the component wrapper when you need the extra attributes. Use `mcpotel.SpanError` directly
when only the error + status is needed (most cases).

### Trace Context Propagation
The router extracts W3C `traceparent` from Envoy headers via `extractTraceContext()`.
This uses `otel.GetTextMapPropagator().Extract()` with a custom `headerCarrier` adapter
for Envoy's `HeaderMap`. Do not manually parse `traceparent` -- use the propagator.

### No-op Span Pattern
In `Process()`, the span is initialized as a no-op via `trace.SpanFromContext(ctx)` and
replaced with a real span when headers arrive. The `defer func() { span.End() }()` closure
captures the variable by reference, so it always ends the correct span. This avoids nil
checks throughout the function.

## Logging Conventions

### Always Use Context-Aware Logging
Use `s.Logger.InfoContext(ctx, ...)` instead of `s.Logger.Info(...)`. The `ctx` parameter
carries the active span, which allows the `TracingHandler` to inject `trace_id` and `span_id`
into log lines automatically.

```go
s.Logger.DebugContext(ctx, "processing request", "tool", toolName)
s.Logger.ErrorContext(ctx, "failed to resolve server", "error", err)
```

### Structured Key-Value Pairs
Use slog's key-value pairs, not `fmt.Sprintf`:

```go
// correct
s.Logger.InfoContext(ctx, "tool resolved", "tool", toolName, "server", serverName)

// avoid
s.Logger.InfoContext(ctx, fmt.Sprintf("tool %s resolved to server %s", toolName, serverName))
```

## Adding OTel to a New Package

1. Create a `tracer()` function with a package-specific tracer name
2. Accept `context.Context` as the first parameter in functions that need tracing
3. Create spans with `tracer().Start(ctx, "package-name.operation")`
4. Use `defer span.End()`
5. Add relevant attributes using OTel semantic conventions
6. Use `s.Logger.InfoContext(ctx, ...)` for log correlation
7. Propagate `ctx` returned by `tracer().Start()` to downstream calls

## Testing Spans

Use `go.opentelemetry.io/otel/sdk/trace/tracetest` (already a dependency) to verify spans:

```go
exporter := tracetest.NewInMemoryExporter()
tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
otel.SetTracerProvider(tp)
t.Cleanup(func() { otel.SetTracerProvider(prev); tp.Shutdown(ctx) })

// ... run code ...

spans := exporter.GetSpans()
// assert span names, attributes, parent-child relationships
```

See `TestProcessSpanEnded` in `internal/mcp-router/server_test.go` for a working example.

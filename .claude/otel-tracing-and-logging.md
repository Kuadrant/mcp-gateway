# OpenTelemetry Tracing and Logging

## Canonical references

Keep the user-facing span, attribute, and metric reference in the
[OpenTelemetry integration guide](../docs/guides/opentelemetry.md). Keep local
stack setup and traffic examples in the [observability example](../examples/otel/README.md).
Do not duplicate their tables here; update those documents when instrumentation
changes.

## Implementation map

OTel setup lives in `internal/otel/`:

- `config.go` reads environment variables and determines which signals are enabled.
- `otel.go` defines `SetupOTelSDK`, the broker tracer name, error helper, propagator, providers, and metrics handler.
- `provider.go` creates the trace exporter, selecting HTTP or gRPC from the URL scheme.
- `logs.go` creates the log exporter with the same scheme dispatch.
- `logging.go` provides `TracingHandler` and `MultiHandler` for trace-correlated stdout and OTLP logs.
- `resource.go` creates shared `service.name`, `service.version`, and VCS resource attributes.

The global tracer provider is set in `cmd/mcp-broker-router/main.go`. The logger
is created with `NewTracingLogger()`.

## Agent-facing invariants

- The router has separate stateful, body-based 2025-11-25 and stateless, header-based 2026-07-28 paths. See the [trace hierarchy](../docs/guides/opentelemetry.md#traces) before changing either path.
- The broker publishes a cached `routing.RoutingTable`; router lookups happen inside route spans and do not create separate lookup spans.
- Use `internaljwt.LogSafeSessionID` for session IDs in telemetry. Do not record raw session IDs in attributes or logs.
- Follow the [OpenTelemetry MCP semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/mcp/) before adding attributes. Keep protocol version fields distinct: incoming header, selected MCP protocol, and JSON-RPC envelope version are different values.
- Trace context must be extracted and propagated through the OpenTelemetry propagator. Do not parse `traceparent` manually.
- Metrics remain enabled independently of OTLP trace and log export.

## Tracer and error conventions

- Use `mcpotel.BrokerTracerName` for broker and upstream tracers.
- Use the router-local `tracerName` constant in `internal/mcp-router/tracing.go`.
- Name spans `<component>.<action>` and always `defer span.End()`.
- Use `mcpotel.SpanError()` for the common `RecordError` plus `SetStatus` operation.
- Use `recordError`, `recordBrokerError`, or `recordBackendError` when their component-specific attributes are needed.

`Process()` starts with a no-op span from `trace.SpanFromContext(ctx)` and replaces
it when request headers arrive. Its deferred closure captures the span variable
by reference, so it ends the active span without nil checks throughout the
function. Preserve this pattern unless the lifecycle changes.

## Logging conventions

Use context-aware slog methods such as `InfoContext`, `DebugContext`, and
`ErrorContext` so the active span can add `trace_id` and `span_id`. Pass structured
key-value pairs directly; do not build log messages with `fmt.Sprintf`.

## Adding OTel to a package

1. Create a package-specific `tracer()` function.
2. Accept `context.Context` where tracing is needed.
3. Start spans with `tracer().Start(ctx, "package-name.operation")`.
4. Add semantic-convention attributes and propagate the returned context.
5. End spans with `defer span.End()`.
6. Use context-aware logging for correlated records.
7. Update the canonical guide and local example when the observable behavior changes.

## Testing spans

Use `go.opentelemetry.io/otel/sdk/trace/tracetest` to assert span names,
attributes, and parent-child relationships. See
`TestProcessSpanEnded` in `internal/mcp-router/ext_proc_adapter_test.go` for a
working example.

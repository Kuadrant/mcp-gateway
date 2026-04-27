# Performance

The broker and router handle every MCP request. Allocations in these paths directly impact GC pressure and throughput under load.

## Anti-patterns

### Value copies from map lookups

`tool, ok := m[key]` on a `map[string]mcp.Tool` copies the entire struct on every lookup. Use `map[string]*mcp.Tool` and return the pointer directly. This was the primary source of GC pressure identified in PR #797.

### Range with value receiver on large structs

`for _, t := range tools` copies each element into `t`. Use `for i := range tools` and index into the slice when the element is large or the loop is hot.

### fmt.Sprintf in log calls

`slog.Info(fmt.Sprintf("msg: %v", val))` allocates even when the log level is disabled. Use structured fields: `logger.Info("msg", "key", val)`.

### INFO logging in per-request paths

`slog.Info` acquires the handler's write mutex. At thousands of req/s this serialises goroutines. Use `logger.Debug` for per-request logging, `logger.Info` for lifecycle events only.

### Unconditional span attribute writes

`span.SetAttributes(...)` allocates even when tracing is not configured. Guard with `if span.IsRecording()`.

### Package-level slog

Always use the injected `logger` instance so log levels are respected and tests can capture output. Never use `slog.Info`/`slog.Error` directly.

## Profiling

pprof is always available on port 6060 in the broker. Load testing and profile capture:

```bash
make perf-run-ramp    # ramp load test with automatic profile capture
make perf-profile     # one-off snapshot
```

See `tests/perf/README.md` for full details and PR #797 for profiling methodology.

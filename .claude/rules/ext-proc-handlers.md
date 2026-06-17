# ext_proc Handler Changes

Read `docs/design/routing.md` before modifying routing or ext_proc logic.

When adding or modifying ext_proc handlers:

- Request handlers go in `request_handlers.go`, response handlers in `response_handlers.go`
- Update `server.go` processing logic
- Add OpenTelemetry span attributes for observability
- Add unit tests with mock ext_proc streams

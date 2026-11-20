# JSON-RPC protocol errors for unknown tool, prompt, and resource

## What changed

The MCP Gateway router now uses a top-level JSON-RPC `error` object for unknown
tools, prompts, and resource routes. Branches that previously returned a tool
`result` with `isError: true` now return these protocol errors:

| Case | Method | Code | Message (typical) |
| --- | --- | --- | --- |
| Unknown tool | `tools/call` | `-32602` | `Tool not found` |
| Unknown prompt | `prompts/get` | `-32602` | `Prompt not found` |
| Missing resource route (2025-11-25) | `resources/read` | `-32002` | `Resource not found` |

These router rejections retain HTTP `200`, using SSE for 2025-11-25 and plain
JSON for 2026-07-28. Unknown 2026-07-28 tools without a matching route continue
to pass to the broker, which returns HTTP `400` with JSON-RPC code `-32602` and
an `unknown tool` message. Clients must read JSON-RPC error bodies on both HTTP
statuses. This PR does not change broker or upstream error handling.

Token-resolution failures for non-elicitation clients and 2026 header/body
mismatches are unchanged: they still use a tool `result` with `isError: true`.

## Who is affected

Any client or middleware that treated unknown tool / prompt / resource as a
successful tool result and inspected `result.isError` (or matched the old text
prefix `MCP error -32602:`). Those paths now expose `error.code` / `error.message`
instead.

## Migration Steps

1. Prefer JSON-RPC `error.code` over `result.isError` for these failures:

```text
Before: parse result.content[0].text / result.isError == true
After:  parse error.code (-32602 tool/prompt, -32002 resource) and error.message
```

2. Update matchers that hard-coded `-32601` for “tool not found” to `-32602`
   (Invalid params). Method-not-found remains `-32601` for unknown MCP methods.

3. For SSE (2025-11-25), read the `data:` JSON payload of the `message` event;
   the error object is inside that JSON, not in the HTTP status line.

4. No cluster or CRD changes are required. Redeploy / upgrade the gateway to a
   build that includes this change, then re-test client error handling against
   a deliberate unknown tool, prompt, and resource URI.

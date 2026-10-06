# Malformed SSE Tool Responses Return a Tool Error

Response guardrails for the 2025-11-25 `tools/call` path now reject malformed SSE responses without silently ending the response. If the upstream returns bare JSON while the response is treated as SSE, or ends an SSE event before its blank-line delimiter, the router discards the malformed upstream bytes and returns a generated SSE tool result with `result.isError: true` and the original client request ID.

The HTTP status and headers remain unchanged because the response body is streamed. If the upstream response has a missing or non-standard `Content-Type`, a client may not recognize the replacement as SSE.

# Malformed SSE Tool Responses Are Re-framed Before Guardrails

Response guardrails for the 2025-11-25 `tools/call` path now repair malformed SSE framing instead of forwarding it. If an upstream returns a bare JSON-RPC body on a response treated as SSE, or ends the final SSE event without its blank-line delimiter, the router re-frames the result as a complete SSE event carrying the client's original request ID before the guardrails check. A body that still cannot be parsed fails closed with a tool result where `result.isError` is `true`.

The HTTP status and headers are unchanged because the response body is streamed. If the upstream response has a missing or non-standard `Content-Type`, a client may not recognize the SSE body.

These repairs, and replacing an upstream JSON-RPC ID that does not match the client's request ID, mask upstream protocol violations from clients. The router logs each repair at debug level with the server name and both IDs, so set the broker-router log level to debug to find a misbehaving upstream.

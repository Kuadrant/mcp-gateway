# Guardrails Configuration Options

- [Secret](#secret)
    - [config.yaml](#configyaml)
- [Response checks](#response-checks)
- [Fail modes](#fail-modes)
- [Body size limits](#body-size-limits)
- [TLS](#tls)
- [Status conditions](#status-conditions)

## Secret

Referenced by the `mcp.kuadrant.io/guardrails-ref` annotation on an [MCPGatewayExtension](./mcpgatewayextension.md). Must be in the same namespace as the MCPGatewayExtension.

| **Field** | **Value** |
|-----------|-----------|
| `type` | `guardrails/external/nemo` |
| `metadata.labels` | `mcp.kuadrant.io/secret: "true"` |
| `data` / `stringData` | `config.yaml` key containing the fields below |

### config.yaml

| **Field** | **Type** | **Required** | **Description** |
|-----------|----------|:------------:|-----------------|
| `url` | String | Yes | NeMo Guardrails server endpoint. Must be an absolute `http` or `https` URL. The host must not be `localhost` or a literal loopback, private, link-local, or unspecified IP address; use a DNS name such as a Service DNS name |
| `configIDs` | []String | No | List of config IDs applied to every server on the gateway. An empty list means only servers with per-server IDs are checked |
| `model` | String | Yes | Model identifier sent on every check |
| `failMode` | String | No | `deny` (default) or `allow`. See [Fail modes](#fail-modes) |

## Response checks

After the backend returns a `tools/call` result, the router sends the joined `content[].text` values to NeMo using the same config IDs as the request. Image and resource content is not sent. A backend JSON-RPC error has no tool text, so it is not checked. A body that cannot be parsed is rejected.

| **NeMo status** | **Client receives** |
|-----------------|---------------------|
| `passed` | The original result |
| `modified` | A tool result whose text is the rewritten content. An upstream `isError: true` result stays an error |
| `blocked` | A tool result with `isError: true` and text `blocked by guardrails` |

A blocked response:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "content": [{"type": "text", "text": "blocked by guardrails"}],
    "isError": true
  }
}
```

For `text/event-stream` responses, earlier SSE events (progress, elicitation) are forwarded as they arrive. Only the final tool result is held for the check. For `application/json`, the whole body is held until the check finishes.

## Fail modes

`failMode` applies when the guardrails server is unreachable, times out, returns a non-2xx status, or returns a body the gateway cannot parse, including a status other than `passed`, `modified`, or `blocked`. Each check waits up to 8 seconds. The setting applies to both global and per-server checks.

| **`failMode`** | **Guardrails server unreachable or unusable** | **Request the gateway cannot translate** |
|----------------|-----------------------------------------------|------------------------------------------|
| `deny` (default) | Rejected. Requests return HTTP 503 with message `guardrails check unavailable`. Responses return an `isError` tool result with the same message | Rejected. Requests return HTTP 400 with message `guardrails check failed`. Responses return an `isError` tool result with the same message |
| `allow` | The call proceeds without a verdict | Rejected, same as `deny` |

> **Note:** `failMode` only covers the guardrails HTTP call. If the router itself is down, Envoy still rejects the request.

## Body size limits

`spec.maxBodyBytes` on the MCPGatewayExtension caps how much of a request or response the router buffers. The default is 5242880 (5 MiB). The controller sets the gateway listener's `per_connection_buffer_limit_bytes` to the same value.

Bodies over the limit are rejected regardless of `failMode`:

- Requests return HTTP 413
- `tools/call` responses that guardrails checks return an `isError` tool result with text `response body exceeds configured size limit`. Responses that are not checked by guardrails do not get this rejection

For SSE responses the limit applies per event, not to the whole stream. For JSON responses it applies to the whole body.

## TLS

HTTPS checks use the system trust store plus the gateway CA bundle from `caCertBundleRef`, when that field is set. There is no separate guardrails CA field, and per-server `caCertSecretRef` does not apply to guardrails. `http` URLs need no TLS configuration.

## Status conditions

| **Resource** | **Condition** | **Reason** | **Meaning** |
|--------------|---------------|------------|-------------|
| MCPGatewayExtension | `GuardrailsResolved` = `True` | `GuardrailsSecretResolved` | The Secret is valid. Tool calls are checked only when at least one config ID is in effect; if the global `configIDs` and a server's per-server IDs are both empty, that server's calls are not checked |
| MCPGatewayExtension | `Ready`, `GuardrailsResolved` = `False` | `GuardrailsSecretNotFound` | The referenced Secret does not exist or lacks the `mcp.kuadrant.io/secret: "true"` label |
| MCPGatewayExtension | `Ready`, `GuardrailsResolved` = `False` | `GuardrailsSecretInvalid` | The Secret has the wrong type or an invalid `config.yaml` |
| MCPServerRegistration | `Ready` = `False` | `GatewayGuardrailsNotConfigured` | The gateway's guardrails Secret is missing or invalid, the server's `mcp.kuadrant.io/guardrails-config-ids` contains no IDs, or the server sets that annotation without `mcp.kuadrant.io/guardrails-ref` on the MCPGatewayExtension. The server is removed from the gateway config |

When `mcp.kuadrant.io/guardrails-ref` is removed or set to an empty value, the controller clears the guardrails config and removes `GuardrailsResolved`.

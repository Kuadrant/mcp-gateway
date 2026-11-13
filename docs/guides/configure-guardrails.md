# Configuring Guardrails for Tool Calls

This guide covers integrating the MCP Gateway with TrustyAI NeMo Guardrails so the gateway inspects `tools/call` traffic. Tool arguments are checked before they reach the backend MCP server, and text in tool results is checked before it reaches the client. If any checked text violates a configured guardrails check, the request or response is rejected.

## Overview

Guardrails are configured at two levels:

- **Global guardrails**: one guardrails server per gateway, referenced from the `MCPGatewayExtension`. Its `configIDs` apply to every MCP server registered on that gateway.
- **Per-server guardrails**: extra NeMo config IDs set on an `MCPServerRegistration`. They are merged with the global list (global IDs first, duplicates dropped). They extend the global policy and cannot remove it.

Other behavior to be aware of:

- **Fail closed by default**: when the guardrails server is unreachable, tool calls are rejected
- **Request and response checks**: arguments are checked before the backend runs, and text in the tool result is checked before the client sees it
- **No client credentials**: client `Authorization` headers are not forwarded to NeMo. NeMo authenticates to its own model backend

For every Secret field, how responses are checked, fail mode behavior, and status conditions, see [Guardrails Configuration Options](../reference/guardrails-configuration-options.md).

## Prerequisites

- MCP Gateway installed with an `MCPGatewayExtension`
- At least one `MCPServerRegistration` on the gateway. See [MCP Server Configuration](./register-mcp-servers.md)
- A NeMo Guardrails server reachable from the broker-router pod, exposing the `/v1/checks` endpoint, with the config IDs you plan to use already loaded

## Set environment variables

Find your MCPGatewayExtension and its namespace:

```bash
kubectl get mcpgatewayextension -A
```

Replace the placeholders with your values before running the following command:

```bash
export MCP_GATEWAY_NS=<MCPGATEWAYEXTENSION_NAMESPACE>
export MCP_GATEWAY_EXT=<MCPGATEWAYEXTENSION_NAME>
export MCP_SERVER_NS=<MCP_SERVER_NS>
export NEMO_URL=<NEMO_URL>

echo "MCP_GATEWAY_NS=$MCP_GATEWAY_NS"
echo "MCP_GATEWAY_EXT=$MCP_GATEWAY_EXT"
echo "MCP_SERVER_NS=$MCP_SERVER_NS"
echo "NEMO_URL=$NEMO_URL"
```

## Configure global guardrails

### Step 1: Create the guardrails Secret

The Secret type must be `guardrails/external/nemo` and it must have the label `mcp.kuadrant.io/secret: "true"`. The `config.yaml` key holds the server settings.

Before deploying this example spec, replace the placeholders in `config.yaml`. Replace `<NEMO_URL>` with your NeMo service endpoint, `$NEMO_URL`. Replace `<NEMO_CONFIG_ID_0>` with the name of the NeMo guardrails config ID you want to enforce on MCP Gateway traffic. If you want more than one guardrails check, you can list any additional NeMo Guardrails config IDs under `configIDs` to enforce them. Replace `<MODEL_NAME>` with the model of your NeMo Guardrails deployment.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: guardrails-config
  namespace: <MCP_GATEWAY_NS>
  labels:
    mcp.kuadrant.io/secret: "true"
type: guardrails/external/nemo
stringData:
  config.yaml: |
    url: <NEMO_URL>
    configIDs:
      - "<NEMO_CONFIG_ID_0>"
    model: "<MODEL_NAME>"
    failMode: deny
```

Verify the Secret:

```bash
kubectl get secret guardrails-config -n $MCP_GATEWAY_NS
```

### Step 2: Reference the Secret from the MCPGatewayExtension

```bash
kubectl annotate mcpgatewayextension $MCP_GATEWAY_EXT -n $MCP_GATEWAY_NS \
  mcp.kuadrant.io/guardrails-ref=guardrails-config
```

The Secret must be in the same namespace as the `MCPGatewayExtension`. The controller validates it and writes the resolved config into the gateway config. The broker-router then starts checking tool calls.

Verify the `GuardrailsResolved` condition:

```bash
kubectl get mcpgatewayextension $MCP_GATEWAY_EXT -n $MCP_GATEWAY_NS \
  -o jsonpath='{.status.conditions[?(@.type=="GuardrailsResolved")]}{"\n"}'
```

`status` should be `True` and `reason` should be `GuardrailsSecretResolved`. `Ready` stays `True` when the rest of the extension is valid.

> **Note:** If the Secret is missing or lacks the `mcp.kuadrant.io/secret: "true"` label, both `Ready` and `GuardrailsResolved` become `False` with reason `GuardrailsSecretNotFound`. A labeled Secret with the wrong type or a bad `config.yaml` uses reason `GuardrailsSecretInvalid`. While either reason is set, every `MCPServerRegistration` on the gateway is `NotReady` with reason `GatewayGuardrailsNotConfigured` and removed from the gateway config, so no tools are served unchecked.

### Step 3: Test a tool call request

The router sends the tool name and arguments to NeMo before routing to the backend. To see a block, send arguments that one of your NeMo configs rejects.

Replace `<GATEWAY_HOST>` with the name of your Gateway listener host, `<TOOL_NAME>` with a tool name from `tools/list` - including its prefix, and `<TOOL_ARGS>` with a JSON object of arguments. This example uses the 2026-07-28 protocol, which needs no session. It only reaches backends that support 2026-07-28; a tool on a backend that supports only 2025-11-25 returns `Tool not found`, so use the 2025-11-25 example below for those:

```bash
curl -X POST https://<GATEWAY_HOST>/mcp \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -H "Mcp-Protocol-Version: 2026-07-28" \
  -H "Mcp-Method: tools/call" \
  -H "Mcp-Name: <TOOL_NAME>" \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/call",
    "params": {
      "name": "<TOOL_NAME>",
      "arguments": <TOOL_ARGS>,
      "_meta": {
        "io.modelcontextprotocol/protocolVersion": "2026-07-28",
        "io.modelcontextprotocol/clientInfo": {"name": "curl", "version": "1.0"},
        "io.modelcontextprotocol/clientCapabilities": {}
      }
    }
  }'
```

For a 2025-11-25 client, first open a session and read the `Mcp-Session-Id` response header:

```bash
curl -i -X POST https://<your-gateway-host>/mcp \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"curl","version":"1.0"}}}'
```

Then send the `tools/call` with `-H "Mcp-Session-Id: <SESSION_ID>"` in place of the `Mcp-Protocol-Version`, `Mcp-Method`, and `Mcp-Name` headers, and without `_meta`.

If the check passes, the response is the normal tool result. A `blocked` verdict returns HTTP 403 with a JSON-RPC error, and the request never reaches the backend:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": -32000,
    "message": "blocked by guardrails"
  }
}
```

If NeMo returns `modified`, the gateway forwards the rewritten arguments to the backend.

Accepted elicitation responses are checked the same way. Decline and cancel responses are not checked.

## Configure per-server guardrails

Use per-server config IDs when some servers need stricter checks than the rest, for example a server that has access to customer data.

### Step 4: Annotate the MCPServerRegistration

Set `mcp.kuadrant.io/guardrails-config-ids` to a comma-separated list of NeMo config IDs:

```bash
kubectl annotate mcpserverregistration <SERVER_NAME> -n $MCP_SERVER_NS \
  mcp.kuadrant.io/guardrails-config-ids=<NEMO_CONFIG_ID_1>,<NEMO_CONFIG_ID_2>
```

Or set it in the resource. For example:

```yaml
apiVersion: mcp.kuadrant.io/v1
kind: MCPServerRegistration
metadata:
  name: <SERVER_NAME>
  namespace: <MCP_SERVER_NS>
  annotations:
    mcp.kuadrant.io/guardrails-config-ids: "<NEMO_CONFIG_ID_1>,<NEMO_CONFIG_ID_2>"
spec:
  # keep your existing spec
  prefix: <PREFIX>
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: <HTTPROUTE_NAME>
```

With the global Secret from Step 1, tool calls to this server are checked with <NEMO_CONFIG_ID_0>, <NEMO_CONFIG_ID_1>, and <NEMO_CONFIG_ID_2>. Tool calls to other servers are still checked with <NEMO_CONFIG_ID_0> only.

> **Note:** Per-server IDs require `mcp.kuadrant.io/guardrails-ref` on the MCPGatewayExtension. Without it, the server is `NotReady` with reason `GatewayGuardrailsNotConfigured`. An annotation that is set but contains no IDs (empty, or only commas and whitespace) is also rejected. To drop per-server IDs, remove the annotation instead of emptying it.

To check only selected servers, leave the global list empty and annotate just those servers:

```yaml
stringData:
  config.yaml: |
    url: <NEMO_URL>
    configIDs: []
    model: "<MODEL_NAME>"
```

The `guardrails-ref` annotation is still required in this case, since it provides the server URL, model, and fail mode.

### Step 5: Verify the server

```bash
kubectl get mcpserverregistration <SERVER_NAME> -n $MCP_SERVER_NS
```

The server should be `Ready`. If it is not, check the reason:

```bash
kubectl get mcpserverregistration <SERVER_NAME> -n $MCP_SERVER_NS \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")]}{"\n"}'
```

Repeat the tool call from Step 3 against a tool with input that only a per-server rail blocks. It should return `blocked by guardrails`, while the same input sent to a tool on another server should pass.

To remove the per-server IDs:

```bash
kubectl annotate mcpserverregistration <SERVER_NAME> -n $MCP_SERVER_NS \
  mcp.kuadrant.io/guardrails-config-ids-
```

The server falls back to the global IDs.

## Optional configuration

### Fail open when the guardrails server is unavailable

By default, tool calls are rejected when the guardrails server cannot be reached. To let them proceed without a check instead, set `failMode: allow` in the existing Secret. The steps below change only `failMode` and keep the current `url`, `configIDs`, and `model`.

Export the current `config.yaml`:

```bash
kubectl get secret guardrails-config -n $MCP_GATEWAY_NS \
  -o jsonpath='{.data.config\.yaml}' | base64 -d > config.yaml
test -s config.yaml || { echo "config.yaml is missing or empty" >&2; exit 1; }
```
> **Note:** If the Secret is missing the `config.yaml` section, do not continue. Instead, inspect it and add the `config.yaml`. If it does not exist, go back to [Step 1](#step-1-create-the-guardrails-secret), to learn how to create the guardrails Secret.

Replace the `failMode` line, or add it if it is missing.

```bash
grep -v '^failMode:' config.yaml > config-new.yaml
echo 'failMode: allow' >> config-new.yaml
```

Check that `config-new.yaml` has your existing `url`, `configIDs`, and `model`, then patch only the `config.yaml` key. The Secret's type and label are unchanged:

```bash
kubectl patch secret guardrails-config -n $MCP_GATEWAY_NS --type merge \
  -p "{\"data\":{\"config.yaml\":\"$(base64 < config-new.yaml | tr -d '\n')\"}}"
```

Verify the new value and that the Secret still resolves:

```bash
kubectl get secret guardrails-config -n $MCP_GATEWAY_NS \
  -o jsonpath='{.data.config\.yaml}' | base64 -d
kubectl get mcpgatewayextension $MCP_GATEWAY_EXT -n $MCP_GATEWAY_NS \
  -o jsonpath='{.status.conditions[?(@.type=="GuardrailsResolved")].status}{"\n"}'
```

The output should show `failMode: allow` and `True`. To fail closed again, repeat these steps with `failMode: deny`.

Requests the gateway cannot translate are still rejected. See [Fail modes](../reference/guardrails-configuration-options.md#fail-modes).

### Increase the body size limit

Requests and responses larger than `spec.maxBodyBytes` (default 5 MiB) are rejected regardless of `failMode`. To raise the limit, for example to 10 MiB:

```bash
kubectl patch mcpgatewayextension $MCP_GATEWAY_EXT -n $MCP_GATEWAY_NS --type merge -p '
spec:
  maxBodyBytes: 10485760
'
```

Verify the new value:

```bash
kubectl get mcpgatewayextension $MCP_GATEWAY_EXT -n $MCP_GATEWAY_NS -o jsonpath='{.spec.maxBodyBytes}{"\n"}'
```

### Use an HTTPS guardrails server

If the guardrails server uses a private or self-signed CA, add that CA to the gateway CA bundle referenced by `caCertBundleRef` on the MCPGatewayExtension. See [Custom CA Certificates](./custom-ca-certificates.md). Then set an `https` `url` in the Secret.

## Removing guardrails

First remove per-server IDs from every server that has them. Servers that still have `mcp.kuadrant.io/guardrails-config-ids` become `NotReady` once the gateway annotation is gone:

```bash
kubectl annotate mcpserverregistration <SERVER_NAME> -n $MCP_SERVER_NS \
  mcp.kuadrant.io/guardrails-config-ids-
```

Then remove the annotation from the MCPGatewayExtension:

```bash
kubectl annotate mcpgatewayextension $MCP_GATEWAY_EXT -n $MCP_GATEWAY_NS \
  mcp.kuadrant.io/guardrails-ref-
```

Verify that `GuardrailsResolved` is gone:

```bash
kubectl get mcpgatewayextension $MCP_GATEWAY_EXT -n $MCP_GATEWAY_NS \
  -o jsonpath='{.status.conditions[?(@.type=="GuardrailsResolved")]}{"\n"}'
```

The command should print an empty line.

> **Note:** Deleting the Secret while the annotation is still set does not disable guardrails. `GuardrailsResolved` stays `False` and every server on the gateway stays `NotReady` until you recreate the Secret or remove the annotation.

## Next Steps

- [Guardrails Configuration Options](../reference/guardrails-configuration-options.md): Learn about configuration options for all Secret fields, response check behavior, and status conditions

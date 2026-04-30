# MCP Gateway policy demo

This sample deploys a small local catalog of MCP servers that can be used to test federation, virtual servers, backend credentials, custom paths, and tool-level authorization together.

It uses the existing test servers in `config/test-servers` and registers five of them with stable prefixes:

| Registration | Prefix | Purpose |
| --- | --- | --- |
| `test-server1` | `test1_` | Go SDK tools such as `greet`, `time`, and `headers` |
| `test-server2` | `test2_` | Go SDK tools such as `hello_world`, `time`, and `headers` |
| `test-server3` | `test3_` | Python FastMCP tools such as `add`, `dozen`, `pi`, and `get_weather` |
| `api-key-server` | `apikey_` | backend credential forwarding through `credentialRef` |
| `custom-path-server` | `custompath_` | MCP served from `/v1/special/mcp` |

## Apply the demo

Start from a local environment created by `make local-env-setup`, then apply the sample:

```bash
kubectl apply -k config/samples/policy-demo
```

Wait for the registrations to become ready:

```bash
kubectl get mcpserverregistration -n mcp-test
kubectl get mcpvirtualserver -n mcp-test
```

The gateway status endpoint should show the registered servers after discovery:

```bash
kubectl port-forward -n mcp-system deploy/mcp-gateway 8080:8080
curl -s http://localhost:8080/status | jq '.servers[] | {name, ready, totalTools}'
```

## Virtual server views

The sample creates three virtual servers:

| Virtual server | Expected tools |
| --- | --- |
| `mcp-test/dev-tools` | `test1_greet`, `test1_headers`, `test2_hello_world`, `test2_headers` |
| `mcp-test/data-tools` | `test2_time`, `test3_time`, `test3_add`, `test3_dozen`, `test3_pi`, `test3_get_weather` |
| `mcp-test/operations-tools` | `apikey_hello_world`, `custompath_echo_custom`, `custompath_path_info`, `custompath_timestamp` |

Use the `X-Mcp-Virtualserver` header when calling `tools/list` to see one focused view:

```bash
curl -s -D /tmp/mcp_headers -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"policy-demo","version":"1.0.0"}}}'

SESSION_ID=$(grep -i "mcp-session-id:" /tmp/mcp_headers | cut -d' ' -f2 | tr -d '\r')

curl -s -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SESSION_ID" \
  -H "X-Mcp-Virtualserver: mcp-test/dev-tools" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' | jq '.result.tools[].name'
```

## Authorization checks

Apply the optional `AuthPolicy` after the authentication guide is working:

```bash
kubectl apply -f config/samples/policy-demo/authpolicy.yaml
```

The policy uses the same role claim shape as the authorization guide. It checks each `tools/call` request against:

- `x-mcp-servername`, set by the router to the selected `MCPServerRegistration`
- `x-mcp-toolname`, set by the router to the unprefixed upstream tool name
- `auth.identity.resource_access`, read from the JWT

With the local Keycloak setup from the authentication guide, the `mcp` user can call allowed tools such as `test1_greet` and `test2_headers`. A call to a tool that is not in the user's roles, such as `test1_time`, should return `403 Forbidden`.

## Remove the demo

```bash
kubectl delete -k config/samples/policy-demo
kubectl delete -f config/samples/policy-demo/authpolicy.yaml --ignore-not-found
```

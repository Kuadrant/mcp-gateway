# OpenShift Local Development Guide

This guide documents the process of deploying the MCP Gateway on OpenShift and connecting a locally running MCP server (e.g., on your laptop) to the cluster. This setup allows you to test local agents against the full cluster environment.

## 1. Deploying to OpenShift

Use the provided helper script to deploy the current OpenShift integration:

```bash
cd config/openshift
./deploy_openshift.sh
```

This script will output the public URL of your MCP Gateway, for example:
`https://mcp.apps.your-cluster-domain.com/mcp`

By default the script installs the MCP Gateway controller through OLM, deploys the gateway instance, and creates an OpenShift Route. Red Hat Connectivity Link is optional and is only installed when `INSTALL_RHCL=true`.

## 2. Verify Internal Gateway Wiring

The current Helm/chart-based OpenShift deployment already configures the internal listener and private host needed for session management. Before connecting an off-cluster MCP server, verify those resources exist instead of patching them manually.

Check the Gateway listeners:

```bash
kubectl get gateway mcp-gateway -n gateway-system -o yaml
```

Expected result:

- a public listener named `mcp`
- an internal listener named `mcps`

Then confirm the broker/router deployment has a private host flag:

```bash
kubectl get deployment mcp-gateway -n mcp-system -o jsonpath='{.spec.template.spec.containers[0].command}'
```

Expected result:

- one command argument starts with `--mcp-gateway-private-host=`

If those resources are present, no extra hairpin patching is needed for the current deployment flow.

## 3. Connecting an External MCP Server

To connect an MCP server running off-cluster (e.g., on your laptop), you need to expose it via a public URL (using tools like `ngrok`, `localtunnel`, or a static IP).

The provided automation script configures the required Istio and Gateway API resources so the gateway can route to that external server.

### Start your Tunnel (Optional)
If you don't have a public IP, start a tunnel. For example, using ngrok:
```bash
ngrok http 8080
```

### Apply Cluster Configuration
Run the connection script and provide your external domain when prompted:

```bash
./config/local-tunnel/connect_external_server.sh
```
*Note: You can pass the domain as an argument: `./config/local-tunnel/connect_external_server.sh my-tunnel.ngrok-free.dev`*

This script automates the creation of:
1. **ServiceEntry:** Registers the external domain and maps port 80 traffic to port 443.
2. **DestinationRule:** Enforces TLS and SNI for the external connection.
3. **HTTPRoute:** Creates `local-tunnel-route` with hostnames for both `local-dev.mcp.local` and your external domain.
4. **MCPServerRegistration:** Creates `local-dev-server` with the `local_` prefix.
5. **Headers:** Adds `ngrok-skip-browser-warning: true` (harmless for non-ngrok domains, but required for ngrok free tier).

## 4. Verification

After applying the configuration, verify the connection and tool discovery using the verification script:

```bash
# Usage: ./utils/verify_mcp_connection.sh [GATEWAY_URL]
./utils/verify_mcp_connection.sh https://mcp.apps.your-cluster.com/mcp
```

Successful output will list the tools discovered from your external server (e.g., `SUCCESS: Found 6 tools with prefix 'local_'!`).

## 5. Client Configuration (Gemini)

### settings.json
Update your `mcpServers` configuration:

```json
"mcpServers": {
  "netedge": {
    "httpUrl": "https://mcp.apps.your-cluster-domain.com/mcp"
  }
}
```

### Handling Self-Signed Certificates
If your OpenShift cluster uses self-signed certificates, launch your client with SSL verification disabled:

```bash
NODE_TLS_REJECT_UNAUTHORIZED=0 gemini
```

## 6. Troubleshooting

1.  **Check Discovery:** Verify the broker logs show tools being discovered:
    `kubectl logs -n mcp-system -l component=broker-router`
    Success: `msg="discovered tools" ... #tools=6`
2.  **Verify Routing:** If you see `404` or `session terminated` errors:
    *   Ensure your `DestinationRule` has the correct `sni` matching your ngrok domain.
    *   Ensure your `DestinationRule` and `ServiceEntry` are in `gateway-system` or have `exportTo: ["*"]`.
3.  **Ngrok Host Header:** Ensure you have the `URLRewrite` filter in your `HTTPRoute`. Ngrok servers reject requests if the `Host` header doesn't match the tunnel domain.
4.  **Internal Listener:** Ensure the gateway still has the internal `mcps` listener and the broker deployment still includes `--mcp-gateway-private-host`.
5.  **Session Invalidation:** If you restart the Broker, all existing sessions are lost. You must restart your client (Gemini) to establish a new session.
6.  **Protocol Mismatch (202 vs 200):** 
    *   **Symptom:** `Streamable HTTP error: Error POSTing to endpoint: `
    *   **Cause:** The `mcp-gateway` (using `mcp-go`) requires SSE and expects `200 OK` for JSON-RPC messages. Newer servers (using official `go-sdk`) may return `202 Accepted` for notifications, which `mcp-gateway` currently treats as an error.
    *   **Fix:** Ensure your local server is configured for SSE (e.g., `stateless: false` in `genmcp`). You may also need to patch your server to return `200 OK` instead of `202 Accepted` for notifications until `mcp-gateway` is updated.
7.  **SSE Requirements:** `mcp-gateway` hardcodes usage of `StreamableHttpClient`. Your backend server MUST support SSE (Server-Sent Events) on the configured endpoint. Verify with `wget` locally - if it returns `405 Method Not Allowed`, SSE is disabled or not supported.

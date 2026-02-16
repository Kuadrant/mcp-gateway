# OpenShift Local Development Guide

This guide documents the process of deploying the MCP Gateway on OpenShift and connecting a locally running MCP server (e.g., on your laptop) to the cluster. This setup allows you to test local agents against the full cluster environment.

## 1. Deploying to OpenShift

Use the provided helper script to deploy the necessary components (Service Mesh, Connectivity Link, and MCP Gateway):

```bash
cd config/openshift
./deploy_openshift.sh
```

This script will output the public URL of your MCP Gateway, for example:
`https://mcp.apps.your-cluster-domain.com/mcp`

## 2. Enabling Internal Session Management

For the MCP Gateway to manage sessions and tool calls correctly, it must be able to communicate with itself using its internal cluster address. By default, OpenShift ingress may block these internal "hairpin" requests.

Execute these commands to allow internal traffic:

### A. Update the Gateway Listener
Allow the Gateway to accept traffic for any hostname (required for internal service-to-service calls) and add a listener on port 8081 for internal traffic (bypassing the ExtProc filter):

```bash
# Allow wildcard hostname on main listener
kubectl patch gateway mcp-gateway -n gateway-system --type='json' -p='[{"op": "remove", "path": "/spec/listeners/0/hostname"}]'

# Add internal listener on port 8081
kubectl patch gateway mcp-gateway -n gateway-system --type='json' -p='[
  {
    "op": "add",
    "path": "/spec/listeners/-",
    "value": {
      "name": "internal-mcp",
      "port": 8081,
      "protocol": "HTTP",
      "allowedRoutes": {"namespaces": {"from": "All"}}
    }
  }
]'
```

### B. Update the Main HTTPRoute
Add the internal service address to the allowed hostnames of the main MCP route:

```bash
kubectl patch httproute mcp-gateway-ingress-httproute -n mcp-system --type='json' -p='[
  {
    "op": "add",
    "path": "/spec/hostnames/-",
    "value": "mcp-gateway-istio.gateway-system.svc.cluster.local"
  }
]'
```

### C. Update the Broker Deployment
Ensure the broker is configured to use the correct internal gateway address for session initialization, and add a HostAlias to route internal traffic correctly through the Gateway.

First, get the ClusterIP of the Gateway:
```bash
GATEWAY_IP=$(oc get svc mcp-gateway-istio -n gateway-system -o jsonpath='{.spec.clusterIP}')
```

Then patch the deployment to use port 8081 (bypassing ExtProc) and map a local alias:
```bash
kubectl patch deployment mcp-gateway -n mcp-system --type='json' -p="[
  {
    \"op\": \"add\",
    \"path\": \"/spec/template/spec/hostAliases\",
    \"value\": [
      {
        \"ip\": \"$GATEWAY_IP\",
        \"hostnames\": [\"local-dev.mcp.local\"]
      }
    ]
  },
  {
    \"op\": \"replace\",
    \"path\": \"/spec/template/spec/containers/0/command/4\",
    \"value\": \"--mcp-gateway-private-host=local-dev.mcp.local:8081\"
  }
]"
```

## 3. Connecting an External MCP Server

To connect an MCP server running off-cluster (e.g., on your laptop), you need to expose it via a public URL (using tools like `ngrok`, `localtunnel`, or a static IP).

The provided automation script helps configure the necessary Istio `ServiceEntry`, `DestinationRule`, and Gateway `HTTPRoute` resources to route traffic from the cluster to your external server.

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
1.  **ServiceEntry:** Registers the external domain and maps port 80 traffic to port 443.
2.  **DestinationRule:** Enforces TLS and SNI for the external connection.
3.  **Service & Route:** Maps internal "hairpin" traffic to the external service.
4.  **Headers:** Adds `ngrok-skip-browser-warning: true` (harmless for non-ngrok domains, but required for ngrok free tier).

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
4.  **Bypass ExtProc:** The Broker must use port 8081 (configured via `internal-mcp` listener) to talk to the Gateway internally, bypassing the EnvoyFilter that rewrites headers.
5.  **Session Invalidation:** If you restart the Broker, all existing sessions are lost. You must restart your client (Gemini) to establish a new session.
6.  **Protocol Mismatch (202 vs 200):** 
    *   **Symptom:** `Streamable HTTP error: Error POSTing to endpoint: `
    *   **Cause:** The `mcp-gateway` (using `mcp-go`) requires SSE and expects `200 OK` for JSON-RPC messages. Newer servers (using official `go-sdk`) may return `202 Accepted` for notifications, which `mcp-gateway` currently treats as an error.
    *   **Fix:** Ensure your local server is configured for SSE (e.g., `stateless: false` in `genmcp`). You may also need to patch your server to return `200 OK` instead of `202 Accepted` for notifications until `mcp-gateway` is updated.
7.  **SSE Requirements:** `mcp-gateway` hardcodes usage of `StreamableHttpClient`. Your backend server MUST support SSE (Server-Sent Events) on the configured endpoint. Verify with `wget` locally - if it returns `405 Method Not Allowed`, SSE is disabled or not supported.

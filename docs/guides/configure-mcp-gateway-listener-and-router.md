
# Configure MCP Gateway Listener and Route

This guide covers adding the required MCP listeners to your existing Gateway. The controller automatically creates an HTTPRoute when the MCPGatewayExtension becomes ready. This guide also covers how to use a custom HTTPRoute if you need CORS headers or additional path rules.

## Prerequisites

- MCP Gateway [installed in your cluster](./quick-start.md)
- Existing [Gateway](https://gateway-api.sigs.k8s.io/) resource
- Gateway API provider (e.g. Istio) configured

## Step 1: Add MCP Listeners to Gateway

### HTTP (multiple listeners)

With HTTP, you can use two listeners on the same port — one for client traffic and one for backend server routing:

- **`mcp`**: the public-facing listener for MCP client traffic. The hostname must resolve to your Gateway's external address.
- **`mcps`**: an internal listener used for routing to registered MCP servers. MCPServerRegistration HTTPRoutes attach to this listener. The hostname is a wildcard that does not need to be publicly resolvable.

```bash
kubectl patch gateway your-gateway-name -n your-gateway-namespace --type merge -p '
spec:
  listeners:
  - name: mcp
    hostname: "mcp.example.com"
    port: 8080
    protocol: HTTP
    allowedRoutes:
      namespaces:
        from: All
  - name: mcps
    hostname: "*.mcp.local"
    port: 8080
    protocol: HTTP
    allowedRoutes:
      namespaces:
        from: All
'
```

Replace `your-gateway-name`, `your-gateway-namespace`, and the `mcp` hostname with your values. The `mcps` wildcard hostname (`*.mcp.local`) is only used for internal cluster routing. You can use any wildcard hostname here (e.g. `*.mcp.internal`).

> **Note:** The patch above replaces all listeners. To preserve existing listeners, use a JSON patch or edit the Gateway directly with `kubectl edit gateway your-gateway-name -n your-gateway-namespace`.

> **Important:** If you installed MCP Gateway using Helm, ensure the `gateway.publicHost` value in your Helm values matches the hostname above. For example:
> ```bash
> helm upgrade mcp-gateway oci://ghcr.io/kuadrant/charts/mcp-gateway \
>   --set gateway.publicHost=mcp.127-0-0-1.sslip.io
> ```

Verify both listeners were added:

```bash
kubectl get gateway your-gateway-name -n your-gateway-namespace -o jsonpath='{.spec.listeners[*].name}'
```

You should see both `mcp` and `mcps` in the output.

### HTTPS (single listener)

With HTTPS, use a **single listener** for both client traffic and backend server routing. Envoy uses TLS SNI to select filter chains, and each HTTPS listener gets its own isolated filter chain with its own route table. When the router handles a `tools/call`, it re-routes the request to the backend by changing the `:authority` header — but this can only reach routes within the same filter chain. A backend HTTPRoute on a separate HTTPS listener is unreachable from the client's filter chain.

```bash
kubectl patch gateway your-gateway-name -n your-gateway-namespace --type merge -p '
spec:
  listeners:
  - name: mcp-tls
    hostname: "*.mcp-gateway.example.com"
    port: 443
    protocol: HTTPS
    tls:
      mode: Terminate
      certificateRefs:
        - kind: Secret
          name: mcp-gateway-tls-cert
    allowedRoutes:
      namespaces:
        from: All
'
```

Use a wildcard hostname so both the public MCP endpoint and backend server HTTPRoutes can attach to the same listener. For example, with `*.mcp-gateway.example.com`, client traffic arrives at `mcp.mcp-gateway.example.com` and backend server routes use hostnames like `server1.mcp-gateway.example.com`.

> **Note:** The patch above replaces all listeners. To preserve existing listeners, use a JSON patch or edit the Gateway directly with `kubectl edit gateway your-gateway-name -n your-gateway-namespace`.

Verify the listener was added:

```bash
kubectl get gateway your-gateway-name -n your-gateway-namespace -o jsonpath='{.spec.listeners[*].name}'
```

You should see `mcp-tls` in the output.

> **Important:** If you installed MCP Gateway using Helm, ensure the `gateway.publicHost` value in your Helm values matches the hostname above. For example:
> ```bash
> helm upgrade mcp-gateway oci://ghcr.io/kuadrant/charts/mcp-gateway \
>   --set gateway.publicHost=mcp.127-0-0-1.sslip.io
> ```

## Step 2: HTTPRoute (Automatic)

The MCPGatewayExtension controller automatically creates an HTTPRoute named `mcp-gateway-route` when the extension becomes ready. The HTTPRoute:
- Routes `/mcp` traffic to the `mcp-gateway` broker service on port 8080
- Uses the hostname from the Gateway listener (wildcards like `*.example.com` become `mcp.example.com`)
- References the target Gateway with the correct `sectionName`
- Is owned by the MCPGatewayExtension and cleaned up automatically on deletion

Verify the HTTPRoute was created:

```bash
kubectl get httproute mcp-gateway-route -n mcp-system
```

### Custom HTTPRoute (Optional)

If you need a custom HTTPRoute (e.g. with CORS headers, additional path rules, or OAuth well-known endpoints), disable automatic creation and manage your own:

1. Find your MCPGatewayExtension name and set `httpRouteManagement: Disabled`:
   ```bash
   kubectl get mcpgatewayextension -n mcp-system
   kubectl patch mcpgatewayextension -n mcp-system your-extension-name \
     --type merge -p '{"spec":{"httpRouteManagement":"Disabled"}}'
   ```

2. Delete the previously auto-created HTTPRoute if it exists:
   ```bash
   kubectl delete httproute mcp-gateway-route -n mcp-system --ignore-not-found
   ```

3. Create your custom HTTPRoute:
   ```bash
   kubectl apply -f - <<EOF
   apiVersion: gateway.networking.k8s.io/v1
   kind: HTTPRoute
   metadata:
     name: mcp-route
     namespace: mcp-system
   spec:
     parentRefs:
       - name: your-gateway-name
         namespace: your-gateway-namespace
         sectionName: mcp
     hostnames:
       - 'mcp.127-0-0-1.sslip.io'
     rules:
       - matches:
           - path:
               type: PathPrefix
               value: /mcp
         filters:
           - type: ResponseHeaderModifier
             responseHeaderModifier:
               add:
                 - name: Access-Control-Allow-Origin
                   value: "*"
                 - name: Access-Control-Allow-Methods
                   value: "GET, POST, PUT, DELETE, OPTIONS, HEAD"
                 - name: Access-Control-Allow-Headers
                   value: "Content-Type, Authorization, Accept, Origin, X-Requested-With"
                 - name: Access-Control-Max-Age
                   value: "3600"
                 - name: Access-Control-Allow-Credentials
                   value: "true"
         backendRefs:
           - name: mcp-gateway
             port: 8080
       - matches:
           - path:
               type: PathPrefix
               value: /.well-known/oauth-protected-resource
         backendRefs:
           - name: mcp-gateway
             port: 8080
   EOF
   ```

## Step 3: Verify EnvoyFilter Configuration

The MCP Gateway controller automatically creates the EnvoyFilter when the MCPGatewayExtension is ready. Check that it exists:

```bash
# EnvoyFilter is created in the Gateway's namespace
kubectl get envoyfilter -n your-gateway-namespace -l app.kubernetes.io/managed-by=mcp-gateway-controller
```

If you see the EnvoyFilter, you can proceed to verification. If the EnvoyFilter is missing:

1. Check that the MCPGatewayExtension is ready:
   ```bash
   kubectl get mcpgatewayextension -n mcp-system
   ```

2. Check the controller logs for errors:
   ```bash
   kubectl logs -n mcp-system deployment/mcp-gateway-controller
   ```

3. Verify the target Gateway exists and the MCPGatewayExtension has proper permissions (ReferenceGrant if cross-namespace).

## Step 4: Verify Configuration

Test that the MCP endpoint is accessible through your Gateway:

```bash
curl -X POST http://mcp.127-0-0-1.sslip.io:8001/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "initialize"}'
```

You should get a response like this:

```json
{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"Kuadrant MCP Gateway","version":"0.0.1"}}}
```

## Next Steps

Now that you have MCP Gateway routing configured, you can connect your MCP servers:

- **[Configure MCP Servers](./register-mcp-servers.md)** - Connect internal MCP servers to the gateway

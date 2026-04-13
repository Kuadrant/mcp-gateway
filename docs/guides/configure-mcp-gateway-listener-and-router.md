
# Configure MCP Gateway Listener and Route

This guide covers adding an MCP listener to your existing Gateway. The controller automatically creates an HTTPRoute when the MCPGatewayExtension becomes ready. This guide also covers how to use a custom HTTPRoute if you need CORS headers or additional path rules.

## Prerequisites

- MCP Gateway [installed in your cluster](./quick-start.md)
- Existing [Gateway](https://gateway-api.sigs.k8s.io/) resource
- Gateway API provider (e.g. Istio) configured

## Step 1: Add MCP Listener to Gateway

Add a listener for MCP traffic to your existing Gateway. Patch it to add a new listener entry:

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
'
```

Replace `your-gateway-name`, `your-gateway-namespace`, and the hostname with your values. The hostname must resolve to your Gateway's external address.

> **Note:** The patch above replaces all listeners. To preserve existing listeners, use a JSON patch or edit the Gateway directly with `kubectl edit gateway your-gateway-name -n your-gateway-namespace`.

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

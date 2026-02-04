
# Configure MCP Gateway Listener and Route

This guide covers adding an MCP listener to your existing Gateway and creating an HTTPRoute to route traffic to the MCP Gateway broker.

## Prerequisites

- MCP Gateway installed in your cluster
- Existing Gateway resource
- Gateway API Provider (Istio) configured

## Step 1: Add MCP Listener to Gateway

Add a listener for MCP traffic to your existing Gateway:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: your-gateway-name
  namespace: your-gateway-namespace
spec:
  gatewayClassName: istio
  listeners:
  # ... your existing listeners ...
  - name: mcp
    hostname: 'mcp.127-0-0-1.sslip.io'  # Change to your hostname
    port: 8080
    protocol: HTTP
    allowedRoutes:
      namespaces:
        from: All
```

> **Important:** If you installed MCP Gateway using Helm, ensure the `gateway.publicHost` value in your Helm values matches the hostname above. For example:
> ```bash
> helm upgrade mcp-gateway oci://ghcr.io/kuadrant/charts/mcp-gateway \
>   --set gateway.publicHost=mcp.127-0-0-1.sslip.io
> ```

## Step 2: Create HTTPRoute

Create an HTTPRoute to route MCP traffic to the broker:

```bash
kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: mcp-route
  namespace: mcp-system
spec:
  parentRefs:
    - name: your-gateway-name        # Change to your Gateway name
      namespace: your-gateway-namespace  # Change to your Gateway namespace
  hostnames:
    - 'mcp.127-0-0-1.sslip.io'              # Match the Gateway listener hostname
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
        - name: mcp-gateway-broker     # MCP Gateway broker service name
          port: 8080
    - matches:
        - path:
            type: PathPrefix
            value: /.well-known/oauth-protected-resource
      backendRefs:
        - name: mcp-gateway-broker
          port: 8080
EOF
```

## Step 3: Verify EnvoyFilter Configuration

Check that the MCP router external processor filter is configured:

```bash
kubectl get envoyfilter mcp-ext-proc -n istio-system
```

If the EnvoyFilter exists, you can proceed to verification. If not, create it:

```bash
kubectl apply -f - <<EOF
apiVersion: networking.istio.io/v1alpha3
kind: EnvoyFilter
metadata:
  name: mcp-ext-proc
  namespace: istio-system
spec:
  workloadSelector:
    labels:
      istio: ingressgateway
  configPatches:
    - applyTo: HTTP_FILTER
      match:
        context: GATEWAY
        listener:
          portNumber: 8080
          filterChain:
            filter:
              name: envoy.filters.network.http_connection_manager
              subFilter:
                name: envoy.filters.http.router
      patch:
        operation: INSERT_FIRST
        value:
          name: envoy.filters.http.ext_proc
          typed_config:
            '@type': type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor
            failure_mode_allow: false
            mutation_rules:
              allow_all_routing: true
            message_timeout: 10s
            processing_mode:
              request_header_mode: SEND
              response_header_mode: SEND
              request_body_mode: BUFFERED
              response_body_mode: NONE
              request_trailer_mode: SKIP
              response_trailer_mode: SKIP
            grpc_service:
              envoy_grpc:
                cluster_name: outbound|50051||mcp-gateway-broker.mcp-system.svc.cluster.local
EOF
```

## Step 4: Verify Configuration

Test that the MCP endpoint is accessible through your Gateway:

```bash
curl -X POST http://mcp.127-0-0-1.sslip.io:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc": "2.0", "id": 1, "method": "initialize"}'
```

You should get a response like this:

```json
{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26","capabilities":{"tools":{"listChanged":true}},"serverInfo":{"name":"Kagenti MCP Broker","version":"0.0.1"}}}
```

## Next Steps

Now that you have MCP Gateway routing configured, you can connect your MCP servers:

- **[Configure MCP Servers](./register-mcp-servers.md)** - Connect internal MCP servers to the gateway


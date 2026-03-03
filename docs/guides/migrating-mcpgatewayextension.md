# Migrating MCPGatewayExtension from v0.5.0

This guide covers migrating an existing MCPGatewayExtension from v0.5.0 to the latest version. Two changes require attention:

>**Note:** this is not intended as an upgrade guide but more to highlight what needs to change. At this stage we don't promise any upgrade path between versions.

1. **`sectionName` is now required** in the `targetRef`
2. **HTTPRoute is now created automatically** by the controller

## sectionName

The `targetRef` field now requires a `sectionName` that identifies which listener on the Gateway the MCP Gateway instance should target. The controller reads the listener's port and hostname to configure the broker-router deployment, EnvoyFilter, and HTTPRoute.

In v0.5.0:

```yaml
apiVersion: mcp.kagenti.com/v1alpha1
kind: MCPGatewayExtension
metadata:
  name: my-mcp-gateway
  namespace: mcp-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    namespace: gateway-system
```

Now:

```yaml
apiVersion: mcp.kagenti.com/v1alpha1
kind: MCPGatewayExtension
metadata:
  name: my-mcp-gateway
  namespace: mcp-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    namespace: gateway-system
    sectionName: mcp  # name of the listener on the Gateway
```

The `sectionName` must match a listener name on the target Gateway:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: mcp-gateway
  namespace: gateway-system
spec:
  gatewayClassName: istio
  listeners:
  - name: mcp          # <-- sectionName references this
    hostname: 'mcp.example.com'
    port: 8080
    protocol: HTTP
    allowedRoutes:
      namespaces:
        from: All
```

With `sectionName` set, several annotations are no longer needed:

| Annotation | Replacement |
|---|---|
| `kuadrant.io/alpha-gateway-listener-port` | Port is read from the listener |
| `kuadrant.io/alpha-gateway-public-host` | Hostname is derived from the listener |

## Automatic HTTPRoute creation

The controller now automatically creates an HTTPRoute named `mcp-gateway-route` when the MCPGatewayExtension becomes ready. The HTTPRoute:

- Routes `/mcp` traffic to the `mcp-gateway` broker service on port 8080
- Uses the hostname from the Gateway listener (wildcards like `*.example.com` become `mcp.example.com`)
- References the target Gateway with the correct `sectionName`
- Is owned by the MCPGatewayExtension and cleaned up automatically on deletion

### If you have an existing HTTPRoute

If you already have a manually created HTTPRoute for the MCP endpoint, you must disable automatic creation to avoid duplicate routes. Add the `kuadrant.io/alpha-disable-httproute` annotation:

```yaml
apiVersion: mcp.kagenti.com/v1alpha1
kind: MCPGatewayExtension
metadata:
  name: my-mcp-gateway
  namespace: mcp-system
  annotations:
    kuadrant.io/alpha-disable-httproute: "true"
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    namespace: gateway-system
    sectionName: mcp
```

This is useful when your HTTPRoute includes custom configuration such as CORS headers, additional path rules, or OAuth well-known endpoints that the auto-generated route does not include.

> **Important:** Setting `kuadrant.io/alpha-disable-httproute: "true"` prevents the controller from creating or updating the HTTPRoute, but does not delete a previously auto-created `mcp-gateway-route`. You must delete it manually once your custom HTTPRoute is in place:
> ```bash
> kubectl delete httproute mcp-gateway-route -n mcp-system
> ```

### If you want to use the auto-generated HTTPRoute

If your existing HTTPRoute has no custom configuration, you can delete it and let the controller manage it:

```bash
# Delete your existing HTTPRoute
kubectl delete httproute my-mcp-route -n mcp-system

# The controller will create mcp-gateway-route automatically
kubectl get httproute mcp-gateway-route -n mcp-system
```

### Helm chart changes

The `httpRoute.create` Helm value has been removed. The controller handles HTTPRoute creation. If you were using `--set httpRoute.create=true` in your Helm commands, remove that flag.

If you need a custom HTTPRoute, set the annotation on the MCPGatewayExtension instead:

```bash
helm upgrade mcp-gateway ./charts/mcp-gateway \
  --namespace mcp-system \
  --set mcpGatewayExtension.create=true \
  --set mcpGatewayExtension.gatewayRef.name=mcp-gateway \
  --set mcpGatewayExtension.gatewayRef.namespace=gateway-system
```

Then annotate the MCPGatewayExtension, delete the auto-created HTTPRoute, and create your custom one:

```bash
kubectl annotate mcpgatewayextension -n mcp-system my-mcp-gateway \
  kuadrant.io/alpha-disable-httproute=true

kubectl delete httproute mcp-gateway-route -n mcp-system --ignore-not-found
kubectl apply -f my-custom-httproute.yaml
```

# Controller

## MCPServerRegistration Resource

```yaml
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: weather-service
  namespace: mcp-test
spec:
  prefix: weather_      # Prefix for federated tools (immutable once set)
  path: /v1/custom/mcp      # Optional custom path (default: /mcp)
  targetRef:                # HTTPRoute reference
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: weather-route
  credentialRef:            # Optional auth
    name: weather-secret
    key: token
```

### Custom Paths

MCPServerRegistration CRD has optional `path` field (defaults to `/mcp`):
- Controller includes full URL with custom path in the config Secret
- Broker successfully connects to custom endpoints and discovers tools
- Router sets `:path` header when path != `/mcp`

**HTTPRoute Requirements**:
- HTTPRoute must have a hostname that matches a Gateway listener
- For internal services, use `*.mcp.local` pattern (matches wildcard listener)
- HTTPRoute should include path match for the custom path

Example:
```yaml
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: custom-path-server
  namespace: mcp-test
spec:
  path: /v1/special/mcp    # Custom endpoint
  prefix: custom_
  targetRef:
    kind: HTTPRoute
    name: custom-path-route
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: custom-path-route
  namespace: mcp-test
spec:
  hostnames:
  - custom.mcp.local       # Must match Gateway listener
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: /v1/special/mcp
    backendRefs:
    - name: custom-mcp-service
      port: 8080
```
- Useful for servers that expose MCP on non-standard endpoints

### External Services
The controller detects external services via two mechanisms:
- **Hostname backendRef**: HTTPRoute with `kind: Hostname` and `group: networking.istio.io` — the hostname is used directly as the upstream URL
- **ExternalName Service**: Kubernetes Service with `spec.type: ExternalName` — the external name is used as the upstream URL

For external services, create appropriate Istio ServiceEntry, DestinationRule, and HTTPRoute resources. See `docs/guides/external-mcp-server.md` for detailed instructions.

## Authentication

MCP servers can require authentication:
1. MCPServerRegistration spec includes `credentialRef` pointing to a Kubernetes secret
   - **Important**: Secret must have label `mcp.kuadrant.io/secret=true`
   - Without this label, the MCPServerRegistration will fail validation
2. Controller reads the credential value and writes it into the per-server config in `mcp-gateway-config` secret
3. Broker reads the credential from the config and uses it for upstream connections
4. Router passes through client headers (including any Authorization header) during session initialization

Example credential secret:
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: weather-secret
  namespace: mcp-test
  labels:
    mcp.kuadrant.io/secret: "true"  # required label
type: Opaque
stringData:
  token: "Bearer your-api-token"
```

Credential updates are handled automatically — when a credential secret changes, the controller rewrites the config secret and the broker re-registers affected servers on the next config reload.

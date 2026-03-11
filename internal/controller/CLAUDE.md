# Controller

## MCPServerRegistration Resource

```yaml
apiVersion: mcp.kagenti.com/v1alpha1
kind: MCPServerRegistration
metadata:
  name: weather-service
  namespace: mcp-test
spec:
  toolPrefix: weather_      # Prefix for federated tools (immutable once set)
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
- Controller includes full URL with custom path in ConfigMap
- Broker successfully connects to custom endpoints and discovers tools
- Router sets `:path` header when path != `/mcp`

**HTTPRoute Requirements**:
- HTTPRoute must have a hostname that matches a Gateway listener
- For internal services, use `*.mcp.local` pattern (matches wildcard listener)
- HTTPRoute should include path match for the custom path

Example:
```yaml
apiVersion: mcp.kagenti.com/v1alpha1
kind: MCPServerRegistration
metadata:
  name: custom-path-server
  namespace: mcp-test
spec:
  path: /v1/special/mcp    # Custom endpoint
  toolPrefix: custom_
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
The controller automatically detects external services. When the HTTPRoute backend name looks like an external hostname (e.g., `api.githubcopilot.com`), the controller uses it directly instead of constructing internal Kubernetes DNS names. Detection criteria:
- Contains dots (.)
- Doesn't end with `.local`, `.svc`, or `.cluster.local`
- Has at least 2 parts when split by dots

For external services, create appropriate Istio ServiceEntry and HTTPRoute resources. See `docs/guides/external-mcp-server.md` for detailed instructions.

## Authentication

MCP servers can require authentication:
1. MCPServerRegistration spec includes `credentialRef` pointing to a Kubernetes secret
   - **Important**: Secret must have label `mcp.kuadrant.io/secret=true`
   - Without this label, the MCPServerRegistration will fail validation
2. Controller aggregates credentials into `mcp-aggregated-credentials` secret
3. Broker receives via environment variables: `KAGENTI_{NAME}_CRED`
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

### Credential Value Change Detection
The system handles credential updates automatically:
1. Controller uses APIReader to bypass cache when reading credential secrets
2. Broker detects credential value changes and re-registers servers automatically
3. Exponential backoff retry for servers with credentials (5s → 10s → 20s → 40s → 60s)

**Timing**:
- Controller → Aggregated Secret: Fast (~5 seconds)
- Aggregated Secret → Volume Mount: 60-120 seconds (Kubernetes kubelet sync limitation)
- Total sync time: ~60-120 seconds

This is a Kubernetes limitation - volume mounts sync every 60s by default and cannot be configured lower.

### OAuth + API Key Conflict (Issue #201)
The router does not currently inject authorization or API key headers into ext_proc responses. Client headers are passed through as-is during session initialization.

# Tutorial: Connecting to an External MCP Server with Authentication

In this tutorial, you will connect MCP Gateway to an external MCP server and enable per-user authentication.

---

## About the Example

This tutorial uses the GitHub MCP server:

https://api.githubcopilot.com/mcp/

You will need a GitHub Personal Access Token with appropriate permissions (for example, `read:user`).

```bash
export GITHUB_PAT="ghp_YOUR_GITHUB_TOKEN_HERE"
```

---

## Step 1: Set Up Environment

If running locally:

```bash
make local-env-setup
make auth-example-setup
```

---

## Step 2: Register the External MCP Server

In this step, you will configure the gateway to route requests to an external MCP server and register it so that tools can be discovered.

### Create ServiceEntry

```bash
kubectl apply -f - <<EOF
apiVersion: networking.istio.io/v1beta1
kind: ServiceEntry
metadata:
  name: github-mcp-external
  namespace: mcp-test
spec:
  hosts:
  - api.githubcopilot.com
  ports:
  - number: 443
    name: https
    protocol: HTTPS
  location: MESH_EXTERNAL
  resolution: DNS
EOF
```

### Create DestinationRule

```bash
kubectl apply -f - <<EOF
apiVersion: networking.istio.io/v1beta1
kind: DestinationRule
metadata:
  name: github-mcp-external
  namespace: mcp-test
spec:
  host: api.githubcopilot.com
  trafficPolicy:
    tls:
      mode: SIMPLE
      sni: api.githubcopilot.com
EOF
```

### Create HTTPRoute

```bash
kubectl apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: github-mcp-external
  namespace: mcp-test
spec:
  parentRefs:
  - group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    namespace: gateway-system
  hostnames:
  - github.mcp.local
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: /mcp
    filters:
    - type: URLRewrite
      urlRewrite:
        hostname: api.githubcopilot.com
    backendRefs:
    - name: api.githubcopilot.com
      kind: Hostname
      group: networking.istio.io
      port: 443
EOF
```

### Create Secret

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: github-token
  namespace: mcp-test
  labels:
    mcp.kuadrant.io/secret: "true"
type: Opaque
stringData:
  token: "Bearer $GITHUB_PAT"
EOF
```

### Create MCPServerRegistration

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1
kind: MCPServerRegistration
metadata:
  name: github
  namespace: mcp-test
spec:
  toolPrefix: github_
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: github-mcp-external
  credentialRef:
    name: github-token
    key: token
EOF
```

---

## Step 3: Connect MCP Client

```json
{
  "mcpServers": {
    "mcp-gateway": {
      "type": "http",
      "url": "http://mcp.127-0-0-1.sslip.io:8001/mcp"
    }
  }
}
```

At this point, tools will be visible, but tool calls will fail because no user credential is being sent to the upstream server.

---

## Step 4: Configure Authentication

### Create AuthPolicy for Gateway

```bash
kubectl apply -f - <<EOF
apiVersion: kuadrant.io/v1
kind: AuthPolicy
metadata:
  name: mcp-auth-policy
  namespace: gateway-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    sectionName: mcp
  when:
    - predicate: "!request.path.contains('/.well-known')"
  rules:
    authentication:
      'keycloak':
        jwt:
          issuerUrl: https://keycloak.127-0-0-1.sslip.io:8002/realms/mcp
    response:
      unauthenticated:
        headers:
          'WWW-Authenticate':
            value: Bearer resource_metadata=http://mcp.127-0-0-1.sslip.io:8001/.well-known/oauth-protected-resource/mcp
        body:
          value: |
            {
              "error": "Unauthorized",
              "message": "Access denied: Authentication required."
            }
      unauthorized:
        code: 401
        headers:
          'WWW-Authenticate':
            value: Bearer resource_metadata=http://mcp.127-0-0-1.sslip.io:8001/.well-known/oauth-protected-resource/mcp
        body:
          value: |
            {
              "error": "Forbidden",
              "message": "Access denied: Unsupported method. New authentication required (401)."
            }
EOF
```

### Create AuthPolicy for External Route

```bash
kubectl apply -f - <<EOF
apiVersion: kuadrant.io/v1
kind: AuthPolicy
metadata:
  name: github-token-replacement-policy
  namespace: mcp-test
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: github-mcp-external
  rules:
    authorization:
      "has-github-pat":
        patternMatching:
          patterns:
          - predicate: |
              request.headers.exists(h, h == "x-github-pat") && request.headers["x-github-pat"] != ""
    response:
      unauthorized:
        code: 400
        body:
          value: |
            {
              "error": "Bad Request",
              "message": "Missing required x-github-pat header"
            }
      success:
        headers:
          "authorization":
            plain:
              expression: |
                "Bearer " + request.headers["x-github-pat"]
EOF
```

---

## Step 5: Add User Credential

```json
{
  "mcpServers": {
    "mcp-gateway": {
      "type": "http",
      "url": "http://mcp.127-0-0-1.sslip.io:8001/mcp",
      "headers": {
        "x-github-pat": "<your-github-pat>"
      }
    }
  }
}
```

---

## Step 6: Verify

- The MCP client should list tools prefixed with `github_`
- Calling a tool (for example, `github_get_me`) should return data associated with your GitHub account

---

## Cleanup

```bash
kubectl delete mcpserverregistration github -n mcp-test
kubectl delete httproute github-mcp-external -n mcp-test
kubectl delete serviceentry github-mcp-external -n mcp-test
kubectl delete destinationrule github-mcp-external -n mcp-test
kubectl delete secret github-token -n mcp-test

kubectl delete authpolicy mcp-auth-policy -n gateway-system --ignore-not-found
kubectl delete authpolicy github-token-replacement-policy -n mcp-test --ignore-not-found
```

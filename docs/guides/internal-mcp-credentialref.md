# Connecting to Private Internal MCP Servers using credentialRef

This guide explains how to connect the MCP Gateway to a private internal MCP server that requires authentication using the `credentialRef` field in the `MCPServerRegistration` resource.


## Overview

When an internal MCP server (running inside your Kubernetes cluster) is protected by authentication (e.g., a static API key or a shared token), the MCP Gateway can securely store and inject these credentials into the request flow.

The process involves:
1. Creating a Kubernetes Secret containing the credential.
2. Labeling the Secret so the MCP Gateway can access it.
3. Referencing the Secret in your `MCPServerRegistration`.


## 1. Create the Credential Secret

First, create a Secret containing your authentication token. 

> [!IMPORTANT]
> The Secret **must** have the label `mcp.kuadrant.io/secret: "true"`. Without this label, the MCP Gateway will not be able to read the credential.

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: api-key-server-secret
  namespace: mcp-test
  labels:
    mcp.kuadrant.io/secret: "true"
type: Opaque
stringData:
  token: "Bearer your-secret-api-key"
EOF
```


## 2. Register the MCP Server

Create the `MCPServerRegistration` resource and point the `credentialRef` to the Secret created above.

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: internal-api-key-server
  namespace: mcp-test
spec:
  toolPrefix: internal_
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: api-key-server-route
  credentialRef:
    name: api-key-server-secret
    key: token
EOF
```


## How it Works

1. **Discovery**: The MCP Gateway controller watches for `MCPServerRegistration` resources. When it sees a `credentialRef`, it verifies the Secret exists and has the required label.
2. **Aggregation**: The controller securely copies the credential into an aggregated secret used by the MCP Gateway components.
3. **Injection**: 
   - **Broker**: Uses the credential to authenticate during the tool discovery phase.
   - **Router**: (Envoy external processor) injects the `Authorization` header into requests sent to the upstream MCP server.
4. **Security**: Credentials are never exposed in the `MCPServerRegistration` spec or in the gateway logs.


## Troubleshooting

- **Validation Error**: If you get a validation error when applying the `MCPServerRegistration`, double-check that your Secret has the `mcp.kuadrant.io/secret: "true"` label.
- **Unauthorized Errors**: If the gateway logs show `401 Unauthorized` when connecting to the upstream server, ensure the `key` in `credentialRef` matches the key used in the Secret's `stringData` (in this example, `token`).

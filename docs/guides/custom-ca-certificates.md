# Custom CA Certificates

This guide covers configuring MCP Gateway to trust upstream MCP servers that use private Certificate Authorities (CAs). This applies when the broker connects to backends using certificates not signed by publicly-trusted CAs.

## Overview

By default, the MCP Gateway broker trusts only publicly-trusted CAs when connecting to upstream MCP servers. In-cluster servers often use private CAs:

- **OpenShift service-serving CA** — automatically signs certificates for in-cluster services
- **cert-manager with a private issuer** — common for internal PKI
- **Self-signed certificates** — development and testing environments

When the broker encounters a server using a private CA, it rejects the connection with a certificate verification error. The `caCertSecretRef` field on `MCPServerRegistration` solves this by providing a custom CA bundle that the broker adds to its trust pool.

> **Note:** This only affects the broker's connections to upstream MCP servers (tool discovery, initialization, session management). Client `tools/call` requests flow through Envoy, which has its own TLS configuration via Gateway API.

## Prerequisites

- MCP Gateway installed and configured
- An upstream MCP server using a private CA
- The CA certificate (PEM format) that signed the server's certificate

## Step 1: Create the CA Certificate Secret

Create a Kubernetes Secret containing the CA certificate PEM data. The Secret must have the label `mcp.kuadrant.io/secret: "true"`.

```bash
kubectl create secret generic my-server-ca \
  --from-file=ca.crt=/path/to/ca-certificate.pem \
  -n mcp-gateway

kubectl label secret my-server-ca \
  mcp.kuadrant.io/secret=true \
  -n mcp-gateway
```

Verify the Secret was created:

```bash
kubectl get secret my-server-ca -n mcp-gateway -o jsonpath='{.metadata.labels}'
```

Expected output should include `mcp.kuadrant.io/secret: "true"`.

### Certificate chains

The CA certificate value can contain a full chain (intermediate and root CAs concatenated in PEM format):

```pem
-----BEGIN CERTIFICATE-----
<IntermediateCA>
-----END CERTIFICATE-----
-----BEGIN CERTIFICATE-----
<RootCA>
-----END CERTIFICATE-----
```

All certificates in the bundle are added to the broker's trust pool alongside the system CAs.

## Step 2: Reference the CA in MCPServerRegistration

Add `caCertSecretRef` to your MCPServerRegistration:

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: my-private-server
  namespace: mcp-gateway
spec:
  targetRef:
    name: my-server-route
  prefix: private_
  caCertSecretRef:
    name: my-server-ca
EOF
```

The `key` field defaults to `ca.crt`. If your Secret uses a different key:

```yaml
spec:
  caCertSecretRef:
    name: my-server-ca
    key: tls.crt
```

## Step 3: Verify the Configuration

Check the MCPServerRegistration status:

```bash
kubectl get mcpserverregistration my-private-server -n mcp-gateway -o jsonpath='{.status.conditions}'
```

A successful configuration shows `Ready: True`. Common errors appear in the status conditions:

| Status message | Cause | Fix |
|----------------|-------|-----|
| CA certificate secret not found | Secret doesn't exist | Create the Secret in the same namespace |
| missing required label | Secret lacks `mcp.kuadrant.io/secret: "true"` | Add the label |
| missing key | The specified key doesn't exist in the Secret | Check the key name matches |
| CA certificate is invalid | PEM data can't be parsed as a certificate | Verify the PEM content is valid |
| exceeds maximum size | CA cert data is larger than 64 KiB | Use a smaller bundle |

## OpenShift Service-Serving CA

OpenShift automatically generates CA certificates for in-cluster services. To use these with MCP Gateway:

```bash
# Extract the service-serving CA bundle
kubectl get configmap/openshift-service-ca.crt \
  -n openshift-config-managed \
  -o jsonpath='{.data.service-ca\.crt}' > /tmp/service-ca.crt

# Create the Secret
kubectl create secret generic service-ca \
  --from-file=ca.crt=/tmp/service-ca.crt \
  -n mcp-gateway

kubectl label secret service-ca \
  mcp.kuadrant.io/secret=true \
  -n mcp-gateway
```

Then reference it in your MCPServerRegistration:

```yaml
spec:
  caCertSecretRef:
    name: service-ca
```

## cert-manager Private Issuer

If your MCP server's certificate is signed by a cert-manager CA issuer, export the CA:

```bash
# Get the CA secret name from the issuer
CA_SECRET=$(kubectl get issuer my-issuer -o jsonpath='{.spec.ca.secretName}')

# Extract the CA certificate
kubectl get secret "$CA_SECRET" -o jsonpath='{.data.ca\.crt}' | base64 -d > /tmp/ca.crt

# Create the MCP Gateway CA secret
kubectl create secret generic my-issuer-ca \
  --from-file=ca.crt=/tmp/ca.crt \
  -n mcp-gateway

kubectl label secret my-issuer-ca \
  mcp.kuadrant.io/secret=true \
  -n mcp-gateway
```

## Using with Credentials

`caCertSecretRef` and `credentialRef` can be used together. They reference separate Secrets:

```yaml
spec:
  targetRef:
    name: my-server-route
  credentialRef:
    name: my-server-token
    key: token
  caCertSecretRef:
    name: my-server-ca
```

## CA Certificate Rotation

When you update the CA certificate Secret, the change propagates automatically:

1. The controller detects the Secret update
2. The MCPServerRegistration is re-reconciled
3. The broker reconnects with the updated CA

End-to-end propagation typically takes 15-30 seconds for controller re-reconciliation and broker reconnection.

## Next Steps

- [Register MCP Servers](./register-mcp-servers.md) — general server registration
- [External MCP Servers](./external-mcp-server.md) — connecting to servers outside the cluster
- [Authentication](./authentication.md) — configuring OAuth for MCP servers

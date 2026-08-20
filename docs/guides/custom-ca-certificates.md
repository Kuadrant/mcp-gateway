# Custom CA Certificates

This guide covers configuring MCP Gateway to trust private Certificate Authorities (CAs). This applies when the broker connects to upstream MCP servers, and when the 2025-11-25 protocol hairpin client connects to the gateway's own HTTPS listener.

## Overview

By default, the MCP Gateway broker and hairpin client trust only publicly-trusted CAs. In-cluster servers and gateway listeners often use private CAs:

- **OpenShift service-serving CA** — automatically signs certificates for in-cluster services
- **cert-manager with a private issuer** — common for internal PKI
- **Self-signed certificates** — development and testing environments

Two fields address this:

- **`caCertBundleRef`** on `MCPGatewayExtension` — a gateway-level CA bundle. The controller writes it once to the config Secret as `gatewayCACertPEM`. The broker uses it as the base trust pool for **all upstream MCP server connections**. The same PEM is also the trust pool for the **2025-11-25 protocol hairpin client** that initializes backends through the gateway HTTPS listener. Use when many servers share the same CA, when the gateway listener uses a private CA, or both (concatenate the PEMs in one Secret).
- **`caCertSecretRef`** on `MCPServerRegistration` — a per-server CA for backends with unique CAs. This appends to the gateway bundle for that server's **upstream** connection only. It is not used for hairpin trust.

Both upstream layers are additive: the broker builds its trust pool from system roots, plus the gateway bundle (if set), plus the per-server CA (if set). Per-server CAs append to the gateway bundle, never replace it.

| Approach | Field | Scope | When to use |
|----------|-------|-------|-------------|
| Gateway bundle | `caCertBundleRef` on MCPGatewayExtension | Upstream connections to all servers, **and** 2025-11-25 hairpin to the gateway HTTPS listener | Shared upstream CA, private gateway listener CA, or both PEMs in one Secret |
| Per-server CA | `caCertSecretRef` on MCPServerRegistration | Single upstream server (not hairpin) | Server has a unique CA not covered by the gateway bundle |

> **Note:** `caCertBundleRef` is used for two TLS paths. (1) Broker to upstream MCP servers — tool discovery, session management, all protocol versions. (2) Hairpin initialize requests from the router back through the gateway HTTPS listener — **2025-11-25 protocol only**. The 2026-07-28 protocol does not hairpin; those `tools/call` requests are routed through Envoy without a gateway-listener TLS client in the broker-router. Client-facing TLS is configured on the Gateway listener, not by this bundle.

If the CA that signed the gateway listener certificate is not the same as the CA that signed upstream server certificates, put **both PEM blocks** in the Secret referenced by `caCertBundleRef`.

## Prerequisites

- MCP Gateway installed and configured
- An upstream MCP server using a private CA
- The CA certificate (PEM format) that signed the server's certificate

## Gateway-Level CA Bundle

### Step 1: Create the CA Bundle Secret

Create a Kubernetes Secret containing the shared CA certificate. The Secret must have the label `mcp.kuadrant.io/secret: "true"`.

```bash
kubectl create secret generic shared-ca-bundle \
  --from-file=ca.crt=/path/to/ca-certificate.pem \
  -n mcp-gateway

kubectl label secret shared-ca-bundle \
  mcp.kuadrant.io/secret=true \
  -n mcp-gateway
```

The CA bundle Secret has a maximum size limit of 256 KiB.

### Step 2: Reference the CA Bundle in MCPGatewayExtension

Add `caCertBundleRef` to your MCPGatewayExtension:

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1
kind: MCPGatewayExtension
metadata:
  name: mcp-gateway
  namespace: mcp-gateway
spec:
  targetRef:
    name: mcp-gateway
    sectionName: mcp
  caCertBundleRef:
    name: shared-ca-bundle
EOF
```

The `key` field defaults to `ca.crt`. If your Secret uses a different key:

```yaml
spec:
  caCertBundleRef:
    name: shared-ca-bundle
    key: tls.crt
```

### Step 3: Verify the Configuration

Check the MCPGatewayExtension status:

```bash
kubectl get mcpgatewayextension mcp-gateway -n mcp-gateway -o jsonpath='{.status.conditions}'
```

A successful configuration shows `Ready: True`. Common errors appear in the status conditions:

| Status message | Cause | Fix |
|----------------|-------|-----|
| CA certificate bundle secret not found | Secret doesn't exist | Create the Secret in the same namespace |
| missing required label | Secret lacks `mcp.kuadrant.io/secret: "true"` | Add the label |
| missing key | The specified key doesn't exist in the Secret | Check the key name matches |
| CA certificate bundle is invalid | PEM data can't be parsed as a certificate | Verify the PEM content is valid |
| exceeds maximum size | CA bundle data is larger than 256 KiB | Use a smaller bundle |

All MCPServerRegistrations in this namespace now trust the gateway-level CA without needing individual `caCertSecretRef` configuration.

> **Note:** The bundle is trust material only; it does not make an upstream HTTPS. The upstream scheme comes from the backend Service port (a port named `https` or with `appProtocol: https`) or from a per-server `caCertSecretRef`. A plain-HTTP backend stays HTTP even when a bundle is configured.

### Gateway Bundle Rotation

When you update the gateway CA bundle Secret, the change propagates automatically:

1. The controller detects the Secret update
2. The MCPGatewayExtension is re-reconciled and the new PEM is written to the config Secret
3. The broker detects the config change, rebuilds the upstream trust pool, reconnects TLS upstream servers, and rebuilds the 2025-11-25 hairpin client. No deployment restart is required.

End-to-end propagation typically takes 60-120 seconds (kubelet volume sync of the config Secret).

### Combining Gateway Bundle with Per-Server CA

When most servers share a CA but some use unique CAs, configure both:

```yaml
# MCPGatewayExtension — shared CA for most servers
apiVersion: mcp.kuadrant.io/v1
kind: MCPGatewayExtension
metadata:
  name: mcp-gateway
  namespace: mcp-gateway
spec:
  targetRef:
    name: mcp-gateway
    sectionName: mcp
  caCertBundleRef:
    name: shared-ca-bundle
---
# MCPServerRegistration — server with a unique CA
apiVersion: mcp.kuadrant.io/v1
kind: MCPServerRegistration
metadata:
  name: special-server
  namespace: mcp-gateway
spec:
  targetRef:
    name: special-server-route
  prefix: special_
  caCertSecretRef:
    name: special-server-ca
```

The broker's trust pool for `special-server` includes: system roots + gateway bundle CA + per-server CA. The per-server CA appends to the gateway bundle, it never replaces it.

## Per-Server CA Certificate

When a single server uses a CA not covered by the gateway bundle (or when no gateway bundle is configured), use `caCertSecretRef` on the MCPServerRegistration.

### Step 1: Create the CA Certificate Secret

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

### Step 2: Reference the CA in MCPServerRegistration

Add `caCertSecretRef` to your MCPServerRegistration:

```bash
kubectl apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1
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

### Step 3: Verify the Configuration

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

## Gateway Listener CA (2025-11-25 hairpin)

When the MCP Gateway listener terminates TLS with a certificate signed by a private CA, **2025-11-25** `tools/call` traffic hairpins an `initialize` request back through that listener. The hairpin HTTP client uses the same `caCertBundleRef` bundle as upstream connections (`gatewayCACertPEM` in the config Secret).

**2026-07-28** MCP calls do not hairpin. They are routed through Envoy using the Gateway listener's own TLS configuration. `caCertBundleRef` is not used for that gateway-listener hop.

If the listener CA is already in the bundle you configured for upstreams, there is nothing else to set. If it is a different CA, concatenate both PEM blocks in the Secret:

```bash
cat /path/to/upstream-ca.pem /path/to/gateway-listener-ca.pem > /tmp/combined-ca.pem

kubectl create secret generic shared-ca-bundle \
  --from-file=ca.crt=/tmp/combined-ca.pem \
  -n mcp-gateway

kubectl label secret shared-ca-bundle \
  mcp.kuadrant.io/secret=true \
  -n mcp-gateway
```

Then set `caCertBundleRef` as in [Gateway-Level CA Bundle](#gateway-level-ca-bundle). The CA bundle Secret has a maximum size limit of 256 KiB.

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

## Per-Server CA Certificate Rotation

When you update a per-server CA certificate Secret, the change propagates automatically:

1. The controller detects the Secret update
2. The MCPServerRegistration is re-reconciled
3. The broker reconnects with the updated CA

End-to-end propagation typically takes 15-30 seconds for controller re-reconciliation and broker reconnection.

For gateway bundle rotation, see [Gateway Bundle Rotation](#gateway-bundle-rotation) above.

## Next Steps

- [Register MCP Servers](./register-mcp-servers.md) — general server registration
- [External MCP Servers](./external-mcp-server.md) — connecting to servers outside the cluster
- [Authentication](./authentication.md) — configuring OAuth for MCP servers

# POC Test Report: kuadrant-operator + mcp-gateway Integration

## Overview

End-to-end test of the umbrella operator pattern for mcp-gateway on OpenShift 4.22
with OLM. The goal was to verify that OLM installs CRDs and RBAC, kuadrant-operator
deploys the mcp-gateway controller via Helm chart, and the controller manages
broker-router resources per MCPGatewayExtension.

---

## Environment

- OpenShift 4.22.4 on AWS (single worker node, EC2)
- OLM version: bundled with OCP 4.22
- Sail Operator 1.30.0 (Istio 1.30)
- All images pushed to `quay.io/pstefans/` (public)

### Images used

| Image | Source | Purpose |
|---|---|---|
| `quay.io/pstefans/kuadrant-operator:latest` | `feature/mcp-gateway-umbrella-poc` branch | Umbrella operator |
| `quay.io/pstefans/kuadrant-operator-bundle:mcp-poc` | Same branch, `bundle/` dir | OLM bundle |
| `quay.io/pstefans/kuadrant-operator-catalog:mcp-poc` | FBC catalog wrapping the bundle | OLM catalog |
| `quay.io/pstefans/mcp-controller:latest` | `feature/umbrella-operator-integration` branch | mcp-gateway controller |
| `quay.io/pstefans/mcp-gateway:latest` | Same branch | mcp-gateway broker-router |
| `ghcr.io/kuadrant/mcp-gateway/test-server1:latest` | mcp-gateway upstream | Upstream test MCP server |

---

## What Deployed What

### Stage 1 — OLM installs from bundle (automatic, no manual steps)

```
OLM reads bundle/manifests/ and applies:
├── CRDs
│     mcpgatewayextensions.mcp.kuadrant.io
│     mcpserverregistrations.mcp.kuadrant.io
│     mcpvirtualservers.mcp.kuadrant.io
│     kuadrants.kuadrant.io
│     authpolicies.kuadrant.io, ratelimitpolicies.kuadrant.io, ...  (all kuadrant CRDs)
├── ClusterRoles
│     kuadrant-operator-mcp-gateway-controller  ← grants mcp-gateway controller RBAC
│     kuadrant-operator-authorino-manager-role
│     kuadrant-operator-dns-operator-manager-role
├── Services
│     kuadrant-operator-wasm (port 8082)  ← serves wasm binary to Gateway pods
│     kuadrant-operator-grpc, kuadrant-operator-metrics
└── kuadrant-operator Deployment (quay.io/pstefans/kuadrant-operator:latest)
```

Trigger: CatalogSource + OperatorGroup + Subscription applied by user.

### Stage 2 — kuadrant-operator deploys on Kuadrant CR creation

```
User creates: Kuadrant CR in kuadrant-system
│
└── MCPGatewayReconciler fires (triggered by Kuadrant CR event)
      renders charts/mcp-gateway via pkg/helm.Renderer (SkipCRDs=true, client-only SSA)
      applies:
        ServiceAccount  kuadrant-operator-mcp-gateway-controller
        ClusterRoleBinding  mcp-gateway-controller
        Deployment  mcp-gateway-controller (quay.io/pstefans/mcp-controller:latest)
```

Also on Kuadrant CR:
- `HelmAuthorinoReconciler` deploys Authorino from `charts/authorino`
- `HelmLimitadorReconciler` deploys Limitador from `charts/limitador`
- `HelmDNSOperatorReconciler` deploys DNS-operator from `charts/dns-operator`

### Stage 3 — mcp-gateway controller deploys per extension

```
User creates: MCPGatewayExtension CR in mcp-test namespace
│
└── MCPGatewayExtensionReconciler fires (watches MCPGatewayExtension)
      validates Gateway target, namespace conflicts, listener conflicts
      creates mcp-gateway-config Secret
      creates mcp-gateway-session-signing-key Secret
      creates ServiceAccount  mcp-gateway  (in mcp-test)
      creates Deployment  mcp-gateway  (broker-router, quay.io/pstefans/mcp-gateway:latest)
      creates Service  mcp-gateway  (ports 8080/50051)
      creates HTTPRoute  mcp-gateway-route  (/mcp, /status, /.well-known)
      creates EnvoyFilter  mcp-ext-proc-mcp-test-gateway  (in istio-system)
      updates Gateway listener status → Ready
```

### Stage 4 — Server registration

```
User creates: MCPServerRegistration CR in mcp-test namespace
│
└── MCPReconciler fires
      resolves upstream URL from HTTPRoute backends
      writes server1 config to mcp-gateway-config Secret
      → broker detects config change, connects to upstream server1
      → server1 tools available via gateway
```

---

## Step-by-Step Test Procedure

### Prerequisites (manual, not part of operator install)

```bash
# 1. Install Istio via Helm
# NOTE: Sail Operator availability varies by OCP cluster. Installing Istio
# directly via Helm is more reliable for POC testing.
# NOTE: Gateway API CRDs are pre-installed on OCP 4.22 — no separate install needed.
helm repo add istio https://istio-release.storage.googleapis.com/charts --force-update

oc new-project istio-system

helm install istio-base istio/base \
  --set defaultRevision=default \
  --namespace=istio-system \
  --version 1.29.1

helm install istiod istio/istiod \
  --namespace=istio-system \
  --version 1.29.1 \
  --wait

# 3. Create GatewayClass and Gateway
# Two listeners are required (from docs/guides/configure-mcp-gateway-listener-and-router.md):
#   mcp  — public-facing, hostname matches the ELB/external address.
#           The broker's HTTPRoute (mcp-gateway-route) and clients attach here.
#   mcps — internal wildcard (*.mcp.local), same port.
#           Upstream MCP server HTTPRoutes attach here (one per MCPServerRegistration).
#           The router hairpins initialize requests back through this listener before
#           forwarding tool calls to the correct upstream. Without this listener
#           MCPServerRegistration reconciliation fails and no tools are discoverable.
GATEWAY_HOST=$(oc get gateway mcp-gateway -n istio-system \
  -o jsonpath='{.status.addresses[0].value}' 2>/dev/null || echo "<elb-hostname>")

oc apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata:
  name: istio
spec:
  controllerName: istio.io/gateway-controller
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: mcp-gateway
  namespace: istio-system
spec:
  gatewayClassName: istio
  listeners:
  - name: mcp
    hostname: "$GATEWAY_HOST"
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
EOF
```

### OLM install (the part being tested)

```bash
# 1. Create namespace and CatalogSource
oc new-project kuadrant-system
oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: pstefans-catalog
  namespace: openshift-marketplace
spec:
  sourceType: grpc
  image: quay.io/pstefans/kuadrant-operator-catalog:mcp-poc
  displayName: PStefans MCP POC
  publisher: pstefans
EOF

# 2. Install operator via OperatorGroup + Subscription
oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: kuadrant-operator-group
  namespace: kuadrant-system
spec:
  upgradeStrategy: Default
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: kuadrant-operator
  namespace: kuadrant-system
spec:
  channel: alpha
  name: kuadrant-operator
  source: pstefans-catalog
  sourceNamespace: openshift-marketplace
  config:
    env:
    - name: MCP_CONTROLLER_IMAGE_REPO
      value: quay.io/pstefans/mcp-controller
    - name: MCP_CONTROLLER_IMAGE_TAG
      value: latest
    - name: MCP_BROKER_ROUTER_IMAGE_REPO
      value: quay.io/pstefans/mcp-gateway
    - name: MCP_BROKER_ROUTER_IMAGE_TAG
      value: latest
    - name: MCP_GATEWAY_CHART_PATH
      value: /charts/mcp-gateway
    - name: RELATED_IMAGE_WASMSHIM
      value: quay.io/kuadrant/wasm-shim:latest
    - name: RELATED_IMAGE_CONSOLE_PLUGIN_LATEST
      value: quay.io/kuadrant/console-plugin:latest
    - name: RELATED_IMAGE_CONSOLE_PLUGIN_PF5
      value: quay.io/kuadrant/console-plugin-pf5:latest
    - name: RELATED_IMAGE_DEVELOPERPORTAL
      value: quay.io/kuadrant/developer-portal-operator:latest
EOF

# 3. Wait for CSV to be created then succeed
# OLM creates the CSV asynchronously after the InstallPlan is approved.
# The CSV may not exist yet when the wait command runs — poll until it appears first.
until oc get csv kuadrant-operator.v0.0.0 -n kuadrant-system &>/dev/null; do
  echo "Waiting for CSV to appear..."; sleep 5
done

oc wait csv/kuadrant-operator.v0.0.0 -n kuadrant-system \
  --for=jsonpath='{.status.phase}'=Succeeded --timeout=180s

# Verify: OLM installed mcp CRDs
oc get crd | grep mcp
# Expected:
#   mcpgatewayextensions.mcp.kuadrant.io
#   mcpserverregistrations.mcp.kuadrant.io
#   mcpvirtualservers.mcp.kuadrant.io

# Verify: OLM installed mcp-gateway controller ClusterRole
oc get clusterrole kuadrant-operator-mcp-gateway-controller
```

### Trigger mcp-gateway controller deployment

```bash
# 4. Create Kuadrant CR (triggers MCPGatewayReconciler)
# NOTE: POC gap - true Independent CR pattern would not require this.
# In production the reconciler should fire on MCPGatewayExtension events alone.
oc apply -f - <<EOF
apiVersion: kuadrant.io/v1beta1
kind: Kuadrant
metadata:
  name: kuadrant
  namespace: kuadrant-system
EOF

# Verify: mcp-gateway controller deployed by kuadrant-operator
oc get deployment mcp-gateway-controller -n kuadrant-system
# Expected: READY 1/1

# Verify in operator logs
oc logs -n kuadrant-system -l control-plane=controller-manager | \
  grep 'mcp gateway controller reconciliation complete'
```

### Create extension and upstream server

```bash
# 5. Create mcp-test namespace, ReferenceGrant, MCPGatewayExtension
oc new-project mcp-test
GATEWAY_HOST=$(oc get gateway mcp-gateway -n istio-system \
  -o jsonpath='{.status.addresses[0].value}')

oc apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1beta1
kind: ReferenceGrant
metadata:
  name: mcp-gateway-grant
  namespace: istio-system
spec:
  from:
  - group: mcp.kuadrant.io
    kind: MCPGatewayExtension
    namespace: mcp-test
  to:
  - group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
---
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPGatewayExtension
metadata:
  name: mcp-gateway
  namespace: mcp-test
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    namespace: istio-system
    sectionName: mcp
  publicHost: $GATEWAY_HOST
EOF

# Verify: broker-router resources created by mcp-gateway controller
oc get deployment,svc,httproute -n mcp-test
# Expected:
#   deployment.apps/mcp-gateway  READY 1/1
#   service/mcp-gateway          ClusterIP  8080/50051
#   httproute/mcp-gateway-route  pointing at $GATEWAY_HOST

# Verify: MCPGatewayExtension is ready
oc get mcpgatewayextension mcp-gateway -n mcp-test \
  -o jsonpath='{.status.conditions[0].reason}'
# Expected: ValidMCPGatewayExtension

# 6. Deploy upstream test server and register it
oc apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mcp-test-server1
  namespace: mcp-test
spec:
  replicas: 1
  selector:
    matchLabels:
      app: mcp-test-server1
  template:
    metadata:
      labels:
        app: mcp-test-server1
    spec:
      containers:
      - name: server
        image: ghcr.io/kuadrant/mcp-gateway/test-server1:latest
        command: ["/mcp-test-server"]
        args: ["--http=:8080"]
        ports:
        - containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: mcp-test-server1
  namespace: mcp-test
spec:
  selector:
    app: mcp-test-server1
  ports:
  - port: 8080
    targetPort: 8080
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: mcp-test-server1
  namespace: mcp-test
spec:
  parentRefs:
  - name: mcp-gateway
    namespace: istio-system
    sectionName: mcps    # upstream server routes attach to mcps, not mcp
  hostnames:
  - server1.mcp.local   # must match *.mcp.local wildcard on the mcps listener
  rules:
  - matches:
    - path:
        type: PathPrefix
        value: /mcp
    backendRefs:
    - name: mcp-test-server1
      port: 8080
---
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: server1
  namespace: mcp-test
spec:
  prefix: server1_
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: mcp-test-server1
EOF

# Verify: MCPServerRegistration is ready
oc get mcpserverregistration server1 -n mcp-test \
  -o jsonpath='{.status.conditions[0].reason}'
# Expected: Ready
```

### End-to-end verification

```bash
GATEWAY_HOST=$(oc get gateway mcp-gateway -n istio-system \
  -o jsonpath='{.status.addresses[0].value}')

# Initialize session (always use a fresh session after any config change)
curl -s -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" \
  -D /tmp/headers.txt \
  -o /tmp/init.txt \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}'

SID=$(grep -i mcp-session-id /tmp/headers.txt | awk '{print $2}' | tr -d '\r\n')

# List tools — should show server1_* tools once MCPServerRegistration is Ready
curl -s -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
# Expected: discover_tools, select_tools, server1_greet, server1_time, server1_slow,
#           server1_headers, server1_add_tool

# Call server1_greet — should return "Hi Patryk"
curl -s -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SID" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"server1_greet","arguments":{"name":"Patryk"}}}'
# Expected (SSE): data: {"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"Hi Patryk"}]}}
```

### MCPVirtualServer — scoped tool subset from registered upstream servers

MCPVirtualServer does not define new inline tools. It holds a named list of tool names
from already-registered upstream servers. The broker filters tools to that list only when
the client sends the `x-mcp-virtualserver: namespace/name` header — it is opt-in per
request, not applied automatically to all sessions.

```bash
# Create a virtual server that exposes only server1_greet and server1_time
# (server1 must already be registered via MCPServerRegistration)
oc apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPVirtualServer
metadata:
  name: my-virtual-server
  namespace: mcp-test
spec:
  tools:
  - server1_greet
  - server1_time
EOF

# Verify it is ready
oc get mcpvirtualserver my-virtual-server -n mcp-test

# Initialize session passing the x-mcp-virtualserver header
# The broker scopes tools to those listed in the MCPVirtualServer
curl -s -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" \
  -H "x-mcp-virtualserver: mcp-test/my-virtual-server" \
  -D /tmp/headers2.txt -o /dev/null \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}'
SID2=$(grep -i mcp-session-id /tmp/headers2.txt | awk '{print $2}' | tr -d '\r\n')

# List tools — only server1_greet and server1_time should appear
curl -s -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SID2" \
  -H "x-mcp-virtualserver: mcp-test/my-virtual-server" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  | grep -o '"name":"[^"]*"'
# Expected: only "name":"server1_greet" and "name":"server1_time"
# (plus discover_tools and select_tools meta-tools)
# server1_headers, server1_slow, server1_add_tool are NOT present

# Without the header, the full tool set is returned (opt-in, not global)
curl -s -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SID" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}' \
  | grep -o '"name":"[^"]*"'
# Expected: all server1_* tools present

# Delete the MCPVirtualServer
oc delete mcpvirtualserver my-virtual-server -n mcp-test
```

### MCPGatewayExtension deletion — broker-router cleanup

> **Note:** Run this test last, or recreate the MCPGatewayExtension and
> MCPServerRegistration before continuing with subsequent tests.

```bash
# Record existing resources
oc get deployment,svc,httproute -n mcp-test | grep mcp-gateway
oc get envoyfilter -n istio-system | grep mcp-test

# Delete the extension
oc delete mcpgatewayextension mcp-gateway -n mcp-test

# Broker-router resources owned by the controller are cascade-deleted via ownerRefs
oc get deployment,svc,httproute -n mcp-test | grep mcp-gateway
# Expected: No resources found

# EnvoyFilter is cross-namespace (istio-system) — controller deletes it explicitly
oc get envoyfilter -n istio-system | grep mcp-test
# Expected: No resources found

# Config secret is cleared (empty config, no servers)
oc get secret mcp-gateway-config -n mcp-test -o jsonpath='{.data.config\.yaml}' \
  | base64 -d | head -5
# Expected: empty servers list

# Recreate the extension and server registration for subsequent tests
GATEWAY_HOST=$(oc get gateway mcp-gateway -n istio-system \
  -o jsonpath='{.status.addresses[0].value}')
oc apply -f - <<EOF
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPGatewayExtension
metadata:
  name: mcp-gateway
  namespace: mcp-test
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    namespace: istio-system
    sectionName: mcp
  publicHost: $GATEWAY_HOST
---
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPServerRegistration
metadata:
  name: server1
  namespace: mcp-test
spec:
  prefix: server1_
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: mcp-test-server1
EOF
oc wait mcpgatewayextension mcp-gateway -n mcp-test \
  --for=jsonpath='{.status.conditions[0].reason}'=ValidMCPGatewayExtension \
  --timeout=60s
```

### Redis session store

```bash
# Deploy Redis
oc apply -f - <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: redis
  namespace: mcp-test
spec:
  replicas: 1
  selector:
    matchLabels:
      app: redis
  template:
    metadata:
      labels:
        app: redis
    spec:
      containers:
      - name: redis
        image: redis:7
        ports:
        - containerPort: 6379
---
apiVersion: v1
kind: Service
metadata:
  name: redis
  namespace: mcp-test
spec:
  selector:
    app: redis
  ports:
  - port: 6379
    targetPort: 6379
---
apiVersion: v1
kind: Secret
metadata:
  name: redis-session-store
  namespace: mcp-test
  labels:
    mcp.kuadrant.io/secret: "true"
stringData:
  CACHE_CONNECTION_STRING: redis://redis.mcp-test.svc.cluster.local:6379
EOF

# Configure extension to use Redis
oc patch mcpgatewayextension mcp-gateway -n mcp-test \
  --type=merge -p '{"spec":{"sessionStore":{"secretName":"redis-session-store"}}}'

# Verify broker-router has the env var injected
oc exec -n mcp-test deploy/mcp-gateway -- env | grep CACHE_CONNECTION_STRING
# Expected: CACHE_CONNECTION_STRING=redis://redis.mcp-test.svc.cluster.local:6379

# Establish a session, then restart the broker — session should survive (stored in Redis)
curl -s -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" \
  -D /tmp/redis_headers.txt -o /dev/null \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}'
SID_REDIS=$(grep -i mcp-session-id /tmp/redis_headers.txt | awk '{print $2}' | tr -d '\r\n')

oc rollout restart deployment/mcp-gateway -n mcp-test
oc rollout status deployment/mcp-gateway -n mcp-test --timeout=60s

# Reuse the same session ID — should still work because session is in Redis
curl -s -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SID_REDIS" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
# Expected: tools list returned (not a new-session error)
```

### Trusted headers keypair

```bash
# Enable auto-generation of the ECDSA P-256 keypair.
# secretName is always required — it is the name of the secret the operator creates.
oc patch mcpgatewayextension mcp-gateway -n mcp-test \
  --type=merge -p '{"spec":{"trustedHeadersKey":{"generate":"Enabled","secretName":"mcp-gateway-trusted-headers-public-key"}}}'

# Verify controller created two secrets
oc get secret -n mcp-test | grep trusted
# Expected:
#   mcp-gateway-trusted-headers-public-key    (public key, mounted in broker)
#   mcp-gateway-trusted-headers-private-key   (private key, broker-only)

# Verify broker-router has TRUSTED_HEADER_PUBLIC_KEY injected
oc exec -n mcp-test deploy/mcp-gateway -- env | grep TRUSTED_HEADER
# Expected: TRUSTED_HEADER_PUBLIC_KEY=<base64 public key>

# Get a fresh session — the broker must restart after the keypair is configured,
# so any session established before the patch will not have signing active
curl -s -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" \
  -D /tmp/th_headers.txt -o /dev/null \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}'
SID_TH=$(grep -i mcp-session-id /tmp/th_headers.txt | awk '{print $2}' | tr -d '\r\n')

# Call the server1_headers tool — it echoes all headers the upstream received.
# X-Mcp-Toolname and X-Mcp-Servername are always set by the router on tool calls.
# With trustedHeadersKey configured, those values are also cryptographically signed
# so the upstream can verify they were not injected by the client.
curl -s -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SID_TH" \
  -d '{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"server1_headers","arguments":{}}}' \
  | grep -o 'X-Mcp-[^"]*'
# Expected output contains:
#   X-Mcp-Toolname: [headers]      — tool name set by router, not client-settable
#   X-Mcp-Servername: [mcp-test/server1]  — upstream server namespace/name
#   X-Mcp-Method: [initialize]     — the hairpin lazy-init request to the upstream
#                                    (router sends initialize before forwarding tool call)
#
# Note: to verify the signature itself, the upstream server must implement
# trusted-header JWT validation using the public key from the secret.

# BYO mode — disable generation and point to a pre-existing secret
# secretName is always required regardless of generate mode
oc patch mcpgatewayextension mcp-gateway -n mcp-test \
  --type=merge -p '{"spec":{"trustedHeadersKey":{"generate":"Disabled","secretName":"my-existing-keypair"}}}'
oc get mcpgatewayextension mcp-gateway -n mcp-test \
  -o jsonpath='{.status.conditions[0].message}'
# Expected: validation error if secret does not exist;
#           ValidMCPGatewayExtension once the secret is present with a "key" entry
```

### URL elicitation

```bash
# Enable URL elicitation on the extension
oc patch mcpgatewayextension mcp-gateway -n mcp-test \
  --type=merge -p '{"spec":{"urlElicitation":"Enabled"}}'

# Verify controller created the tokens HTTPRoute
oc get httproute mcp-gateway-tokens-route -n mcp-test
# Expected: httproute exists

# Attempt a tool call on a server that requires auth — router returns token URL
curl -sv -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SID" \
  -d '{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"server1_greet","arguments":{"name":"test"}}}' \
  2>&1 | grep -E 'HTTP|location|token|elicit'

# Disable again — tokens HTTPRoute should be deleted
oc patch mcpgatewayextension mcp-gateway -n mcp-test \
  --type=merge -p '{"spec":{"urlElicitation":"Disabled"}}'
oc get httproute mcp-gateway-tokens-route -n mcp-test
# Expected: not found
```

---

## Test Results

### OLM install (Stage 1)

| Check | Result |
|---|---|
| `mcpgatewayextensions` CRD installed by OLM | PASS |
| `mcpserverregistrations` CRD installed by OLM | PASS |
| `mcpvirtualservers` CRD installed by OLM | PASS |
| `kuadrant-operator-mcp-gateway-controller` ClusterRole installed by OLM | PASS |
| `kuadrant-operator-wasm` Service installed by OLM | PASS |
| kuadrant-operator Deployment running our custom image | PASS |

### Controller deployment (Stage 2)

| Check | Result |
|---|---|
| MCPGatewayExtension CRD detected at boot | PASS |
| `mcpgatewayextension watcher` registered | PASS |
| Kuadrant CR triggers `MCPGatewayReconciler` | PASS |
| `mcp-gateway-controller` Deployment created in kuadrant-system | PASS |
| `mcp-gateway-controller` ClusterRoleBinding created | PASS |
| Controller ServiceAccount created | PASS |

### Extension reconciliation (Stage 3)

| Check | Result |
|---|---|
| `mcp-gateway` broker-router Deployment created in mcp-test | PASS |
| `mcp-gateway` Service (8080/50051) created | PASS |
| `mcp-gateway-route` HTTPRoute created | PASS |
| `mcp-ext-proc-mcp-test-gateway` EnvoyFilter created in istio-system | PASS |
| MCPGatewayExtension status: `ValidMCPGatewayExtension` | PASS |

### Tool calls (Stage 4)

| Check | Result |
|---|---|
| `server1_greet("Patryk")` → `"Hi Patryk"` | PASS |
| `server1_time()` → current UTC time | PASS |
| `tools/list` shows 5 server1_* tools | PASS |

### Auth enforcement (AuthPolicy)

| Check | Result |
|---|---|
| No auth token → HTTP 401 | PASS |
| Wrong token → HTTP 401 | PASS |
| Valid token → HTTP 200 + session | PASS |
| Tool call with valid auth → `"Hi Patryk"` | PASS |

### MCPVirtualServer

| Check | Result |
|---|---|
| `tools/list` with `x-mcp-virtualserver` header returns only named tools | |
| `tools/list` without header returns full tool set (opt-in, not global) | |
| Deleting MCPVirtualServer causes filter to stop applying | |

### MCPGatewayExtension deletion cleanup

| Check | Result |
|---|---|
| Broker-router Deployment deleted on extension delete (ownerRef cascade) | |
| Service deleted on extension delete | |
| HTTPRoute deleted on extension delete | |
| EnvoyFilter deleted in istio-system (explicit cross-namespace delete) | |
| Config secret cleared to empty on extension delete | |

### Redis session store

| Check | Result |
|---|---|
| `CACHE_CONNECTION_STRING` env var injected into broker-router | |
| Session survives broker-router pod restart (stored in Redis) | |

### Trusted headers keypair

| Check | Result |
|---|---|
| Auto-generated keypair creates two secrets (public + private) | |
| `TRUSTED_HEADER_PUBLIC_KEY` env var injected into broker-router | |
| `X-Mcp-Toolname` header present in upstream-received headers | |
| `X-Mcp-Servername` header present with correct `namespace/name` value | |
| `X-Mcp-Method: initialize` confirms router hairpin lazy-init pattern | |
| BYO mode validates referenced secret exists | |

### URL elicitation

| Check | Result |
|---|---|
| `mcp-gateway-tokens-route` HTTPRoute created when `urlElicitation=Enabled` | |
| Tokens HTTPRoute deleted when `urlElicitation=Disabled` | |

---


### Gaps not fixed (POC scope)

| Gap | Description | Production fix |
|---|---|---|
| Kuadrant CR required | `MCPGatewayReconciler` is subscribed to Kuadrant events; controller not deployed until Kuadrant CR exists | Wire directly to MCPGatewayExtension events (Independent CR pattern) |
| Istio extension provider | Must be set in Istio CR before auth works; not automated | `AuthorinoIstioIntegrationReconciler` should configure this when Istio + Authorino are both present |
| ReferenceGrant | Cross-namespace Gateway references require explicit ReferenceGrant | This is correct Gateway API behaviour; users should create it as part of MCPGatewayExtension setup |
| CRD sync mechanism | mcp-gateway CRDs are manually copied into kuadrant-operator bundle | `make sync-mcp-crds` target fetching from mcp-gateway release tag |

---

## What Was Not Tested

- Multi-extension (multiple MCPGatewayExtensions in different namespaces) — controller enforces one extension per listener; requires either a second listener or separate Gateway per team
- OLM upgrade path (version bump)
- Non-OLM (Any:Kube) install path

See [olm-upgrade-guide.md](./olm-upgrade-guide.md) for the
full zero-downtime migration procedure and verification steps.

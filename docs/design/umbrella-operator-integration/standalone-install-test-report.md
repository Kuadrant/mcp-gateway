# Standalone Install Test Report: mcp-gateway on Kind

## Overview

End-to-end test of the standalone Helm installation of mcp-gateway on a local Kind
cluster. The goal is to verify that the Helm chart installs CRDs, deploys the controller,
and that the controller manages all per-MCPGatewayExtension resources without any external
operator.

This is the contrast to the OLM install — in standalone mode there is no kuadrant-operator.
The mcp-gateway controller owns everything: CRDs (via Helm `crds/`), controller Deployment,
broker-router Deployment, EnvoyFilter, secrets, and Gateway status.

---

## Environment

- Kind 0.27.0, Kubernetes 1.33.1, single-node cluster
- Istio 1.26.3 (via Sail Operator, Helm-managed)
- MetalLB (provides LoadBalancer IPs to Istio Gateway)
- All images pulled from `ghcr.io/kuadrant/`

### Cluster port mappings (Kind NodePort)

| Host port | Purpose |
|---|---|
| `8001` | mcp-gateway (primary, NodePort → 30080) |
| `8002` | Keycloak / auth testing |
| `8003`–`8006` | Additional e2e gateways |

Client MCP endpoint: `http://mcp.127-0-0-1.sslip.io:8001/mcp`

---

## What Deployed What

### Stage 1 — Infrastructure (Makefile targets)

```
make setup-cluster-base
│
├── kind-create-cluster       Creates Kind cluster (config/kind/cluster.yaml)
├── build-and-load-image      Builds mcp-gateway + mcp-controller images, loads into Kind
├── gateway-api-install       Installs Gateway API CRDs (standard channel)
├── istio-install             Installs Istio via Sail Operator Helm chart
└── metallb-install           Installs MetalLB for LoadBalancer support
```

### Stage 2 — mcp-gateway install (Makefile targets)

```
make deploy
│
├── install-crd               kubectl apply of config/crd/  (all 3 mcp CRDs)
└── deploy-controller         kubectl apply of config/mcp-gateway/overlays/mcp-system/
                                → Namespace mcp-system
                                → ServiceAccount mcp-gateway-controller
                                → ClusterRole + ClusterRoleBinding
                                → Deployment mcp-gateway-controller (DISABLE_INFRASTRUCTURE_RECONCILER=false)
                                → MCPGatewayExtension CR (mcp-gateway-extension)
```

The MCPGatewayExtension CR created in Stage 2 immediately triggers the controller.

### Stage 3 — Controller creates broker-router (automatic, no user action)

```
MCPGatewayExtensionReconciler fires on MCPGatewayExtension create
│
├── validates Gateway target (gateway-system/mcp-gateway, listener: mcp)
├── checks namespace + listener conflicts
├── creates Secret  mcp-gateway-config           (in mcp-system)
├── creates Secret  mcp-gateway-session-signing-key  (in mcp-system)
├── creates ServiceAccount  mcp-gateway          (in mcp-system)
├── creates Deployment  mcp-gateway              (broker-router, in mcp-system)
├── creates Service  mcp-gateway                 (8080/50051, in mcp-system)
├── creates HTTPRoute  mcp-gateway-route         (/mcp /status /.well-known, in mcp-system)
├── creates EnvoyFilter  mcp-ext-proc-...        (in istio-system)
└── updates Gateway listener status → Ready
```

### Stage 4 — Gateway created separately

```
make deploy-gateway
│
└── kubectl apply of config/istio/gateway/
      → Gateway  mcp-gateway  (in gateway-system)
          listeners:
          - name: mcp    port 8080 HTTP  (public, hostname: mcp.127-0-0-1.sslip.io)
          - name: mcps   port 8080 HTTP  (internal wildcard: *.mcp.local)
```

In standalone mode the Gateway is a prerequisite — the MCPGatewayExtension targets it.
The Helm chart can optionally create it (`gateway.create: true`).

### Stage 5 — Test servers and registrations

```
make deploy-example
│
├── kind-pull-test-servers    Pulls ghcr.io/kuadrant/mcp-gateway/test-server* into Kind
└── kubectl apply config/test-servers/ + config/samples/
      Each server:
        Deployment + Service + HTTPRoute (attaches to mcps listener, hostname *.mcp.local)
      MCPServerRegistration per server → controller writes server config to broker
```

---

## Step-by-Step Procedure

### Prerequisites

- Docker running
- `kind`, `kubectl`, `helm`, `make` installed

### 1. Create Kind cluster and install infrastructure

```bash
git clone https://github.com/Kuadrant/mcp-gateway
cd mcp-gateway

# Create cluster with NodePort mappings and install Istio + MetalLB + Gateway API
make setup-cluster-base
```

Expected: Kind cluster `mcp-gateway` running, `istio-system` and `gateway-system` namespaces active.

Verification:
```bash
kubectl get nodes
# NAME                        STATUS   ROLES           AGE
# mcp-gateway-control-plane   Ready    control-plane   ...

kubectl get pod -n istio-system
# istiod-... Running
```

### 2. Install mcp-gateway via kustomize

```bash
make deploy
```

This applies the CRDs, deploys the controller, and creates the MCPGatewayExtension CR in `mcp-system`.

Verification:
```bash
# CRDs installed
kubectl get crd | grep mcp
# mcpgatewayextensions.mcp.kuadrant.io
# mcpserverregistrations.mcp.kuadrant.io
# mcpvirtualservers.mcp.kuadrant.io

# Controller running
kubectl get deployment mcp-gateway-controller -n mcp-system
# READY 1/1

# MCPGatewayExtension reconciled
kubectl get mcpgatewayextension -n mcp-system
# NAME                    READY   ...
# mcp-gateway-extension   True
```

### 3. Deploy the Gateway

```bash
make deploy-gateway
```

Verification:
```bash
kubectl get gateway -n gateway-system
# NAME          CLASS   ADDRESS         PROGRAMMED
# mcp-gateway   istio   192.168.97.0    True
```

### 4. Verify broker-router created by controller

```bash
kubectl get deployment,svc,httproute,secret -n mcp-system
```

Expected resources created by the controller (not by Helm or kustomize directly):

| Resource | Name | Created by |
|---|---|---|
| Deployment | `mcp-gateway` | MCPGatewayExtensionReconciler |
| Service | `mcp-gateway` (8080/50051) | MCPGatewayExtensionReconciler |
| HTTPRoute | `mcp-gateway-route` | MCPGatewayExtensionReconciler |
| Secret | `mcp-gateway-config` | MCPGatewayExtensionReconciler |
| Secret | `mcp-gateway-session-signing-key` | MCPGatewayExtensionReconciler |
| EnvoyFilter | `mcp-ext-proc-mcp-system-gateway` (in istio-system) | MCPGatewayExtensionReconciler |

### 5. Deploy test servers

```bash
make deploy-example
```

Pulls test server images into Kind and applies Deployments, Services, HTTPRoutes,
and MCPServerRegistration resources in `mcp-test`.

Verification:
```bash
kubectl get mcpserverregistration -n mcp-test
# All READY=True within ~30s of deployment
```

---

## End-to-End Verification

### Tool discovery

```bash
# Initialize session
curl -s -X POST "http://mcp.127-0-0-1.sslip.io:8001/mcp" \
  -H "Content-Type: application/json" \
  -D /tmp/headers.txt \
  -o /tmp/init.txt \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{
    "protocolVersion":"2024-11-05",
    "capabilities":{},
    "clientInfo":{"name":"test","version":"1.0"}
  }}'

SID=$(grep -i mcp-session-id /tmp/headers.txt | awk '{print $2}' | tr -d '\r\n')

# List tools
curl -s -X POST "http://mcp.127-0-0-1.sslip.io:8001/mcp" \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}'
```

### Tool calls

```bash
# Call test1_greet
curl -s -X POST "http://mcp.127-0-0-1.sslip.io:8001/mcp" \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SID" \
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{
    "name":"test1_greet",
    "arguments":{"name":"Patryk"}
  }}'
# Expected (SSE): data: {"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"Hi Patryk"}]}}

# Call test1_time
curl -s -X POST "http://mcp.127-0-0-1.sslip.io:8001/mcp" \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SID" \
  -d '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{
    "name":"test1_time",
    "arguments":{}
  }}'
# Expected (SSE): data: {"jsonrpc":"2.0","id":4,"result":{"content":[{"type":"text","text":"2026-..."}]}}
```

---

## Test Results

### Infrastructure (Stage 1)

| Check | Result |
|---|---|
| Kind cluster created | PASS |
| Istio istiod running | PASS |
| MetalLB assigns LoadBalancer IP | PASS |
| Gateway API CRDs installed | PASS |

### mcp-gateway install (Stage 2)

| Check | Result |
|---|---|
| `mcpgatewayextensions` CRD installed by Helm crds/ | PASS |
| `mcpserverregistrations` CRD installed by Helm crds/ | PASS |
| `mcpvirtualservers` CRD installed by Helm crds/ | PASS |
| Controller ClusterRole created | PASS |
| Controller Deployment running | PASS |

### Controller creates broker-router (Stage 3 — no user action)

| Check | Result |
|---|---|
| `mcp-gateway` broker-router Deployment created in mcp-system | PASS |
| `mcp-gateway` Service (8080/50051) created | PASS |
| `mcp-gateway-route` HTTPRoute created | PASS |
| `mcp-gateway-config` Secret created | PASS |
| `mcp-gateway-session-signing-key` Secret created | PASS |
| `mcp-ext-proc-...` EnvoyFilter created in istio-system | PASS |
| MCPGatewayExtension status: `ValidMCPGatewayExtension` | PASS |

### Server registration (Stage 5)

| Check | Result |
|---|---|
| 9 MCPServerRegistrations all Ready | PASS |
| 42 total tools aggregated across 8 servers | PASS |

### Tool calls

| Check | Result |
|---|---|
| `test1_greet("Patryk")` → `"Hi Patryk"` | PASS |
| `test1_time()` → current UTC time | PASS |
| Session established with JWT session ID | PASS |
| All tool calls routed through Envoy ext_proc | PASS |

---

## Comparison: Standalone vs OLM Install

| Concern | Standalone (Kind/Helm) | OLM (kuadrant-operator) |
|---|---|---|
| **CRDs** | Helm `crds/` dir — applied by `helm install` | OLM bundle — installed from `bundle/manifests/` |
| **Controller Deployment** | kustomize overlay / Helm chart templates | kuadrant-operator renders `charts/mcp-gateway` via Helm SSA |
| **Controller ClusterRole** | kustomize / Helm `templates/rbac.yaml` | OLM bundle `bundle/manifests/kuadrant-operator-mcp-gateway-controller-role_...yaml` |
| **Broker-router Deployment** | mcp-gateway controller (all infra in one place) | mcp-gateway controller (identical — both modes run controller in full mode) |
| **Broker-router Service** | mcp-gateway controller | mcp-gateway controller |
| **HTTPRoutes** | mcp-gateway controller | mcp-gateway controller |
| **EnvoyFilter** | mcp-gateway controller | mcp-gateway controller |
| **Session signing key** | mcp-gateway controller | mcp-gateway controller |
| **Config secret** | mcp-gateway controller | mcp-gateway controller |
| **Trigger for reconciliation** | MCPGatewayExtension CR | Kuadrant CR (POC) → MCPGatewayExtension CR (target) |
| **Prerequisites** | Istio + Gateway API | Istio + Gateway API + Kuadrant CR |
| **Manual steps after install** | Create MCPServerRegistration per server | Create MCPGatewayExtension + ReferenceGrant + MCPServerRegistration |

### Key difference

In standalone mode the mcp-gateway controller runs in full mode (`DISABLE_INFRASTRUCTURE_RECONCILER=false`) and creates all resources. In OLM mode it runs identically — the only difference is who deploys the controller itself (Helm vs kuadrant-operator). Once running, the controller behaves exactly the same in both modes.

---

## Known Gotcha: gateway.namespace must match your Istio install namespace

The Helm chart default is `gateway.namespace: gateway-system`. If Istio is installed
in `istio-system` (as is common on OCP), the controller derives the broker's
`--mcp-gateway-private-host` flag from the wrong namespace, producing:

```
dial tcp: lookup mcp-gateway-istio.gateway-system.svc.cluster.local: no such host
```

Tool calls fail immediately. Fix by passing the correct namespace at install time:

```bash
helm upgrade -i mcp-gateway oci://ghcr.io/kuadrant/charts/mcp-gateway \
  --set gateway.namespace=istio-system \   # match your actual Istio namespace
  --set mcpGatewayExtension.gatewayRef.namespace=istio-system \
  ...
```

This is not specific to any migration path — it affects any standalone Helm install
where Istio is not in `gateway-system`.

---

## What Was Not Tested

- URL elicitation (`spec.urlElicitation=Enabled`)
- Redis session store (`spec.sessionStore`)
- Trusted headers keypair generation (`spec.trustedHeadersKey.generate=Enabled`)
- AuthPolicy on standalone (requires Authorino)
- MCPVirtualServer (inline tools/prompts)
- MCPGatewayExtension deletion cleanup (ownerRef cascade via controller)
- TLS server (`make deploy-tls-test-server`, requires cert-manager)
- Helm install path (`helm upgrade -i mcp-gateway oci://ghcr.io/kuadrant/charts/mcp-gateway`) vs kustomize path used above

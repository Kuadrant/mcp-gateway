# Zero-Downtime Upgrade: kuadrant-operator + mcp-gateway via OLM

This guide walks through upgrading from kuadrant-operator v1.5.0 + mcp-gateway v0.7.1
(both installed via OLM) to kuadrant-operator v1.5.1-poc (which takes over mcp-gateway CRD
and controller management), with no interruption to MCP traffic.

---

## Overview

**Before (v1 — both installed via OLM):**
- kuadrant-operator v1.5.0 from `kuadrant-full-catalog` (`quay.io/pstefans/kuadrant-full-catalog:latest`) — manages Kuadrant policies
- mcp-gateway v0.7.1 from `mcp-gateway-catalog` (`ghcr.io/kuadrant/mcp-controller-catalog:v0.7.1`) — owns mcp CRDs and controller

**After (v2 — umbrella pattern):**
- kuadrant-operator v1.5.1-poc from same `kuadrant-full-catalog` — takes over mcp CRDs and controller
- mcp-gateway OLM subscription removed — kuadrant-operator manages everything

**Why the POC is versioned v1.5.1:**
The mcp-gateway v0.7.1 bundle declares `olm.package.required: kuadrant-operator >= 1.4.3`.
As long as the mcp-gateway subscription exists, OLM keeps kuadrant-operator v1.5.0 installed
to satisfy this constraint. A replacement CSV must also satisfy `>= 1.4.3` — so the POC is
versioned `v1.5.1` (not `v0.0.0`). OLM's `replaces: kuadrant-operator.v1.5.0` upgrade edge
then handles the transition cleanly without version conflicts.

**This is identical to the production upgrade path.** In production there is a single
catalog that ships successive versions — v1.5.0 then v1.5.1 in the same channel with the
same `replaces` edge. The user's subscription stays pointed at the same catalog; OLM detects
the new version and generates the upgrade InstallPlan automatically. The `kuadrant-full-catalog`
used in this guide is exactly that pattern — both versions in one catalog, one subscription,
one upgrade path.

**Why zero-downtime is achievable:** broker-router resources (Deployment, Service,
ServiceAccount, HTTPRoute, EnvoyFilter) are owned by the MCPGatewayExtension CR via
ownerRef, not by the operator CSV. Deleting or replacing the CSV does not affect the
broker-router.

---

## Prerequisites

- OpenShift 4.22+ with OLM
- Gateway API CRDs present (pre-installed on OCP 4.22)
- `helm` installed locally

---

## Stage 1 — Install v1 baseline (kuadrant-operator + mcp-gateway via OLM)

### 1a. Install Istio

Istio must be installed first. The mcp-gateway controller requires the
`envoyfilters.networking.istio.io` CRD, and kuadrant-operator watches Istio resources
at startup.

```bash
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
```

### 1b. Add catalog sources

Two catalogs are required. `kuadrant-full-catalog` is a combined catalog containing both
kuadrant-operator v1.5.0 and v1.5.1 in the same channel — this allows OLM to perform the
upgrade natively without the `@existing` constraint workaround. `mcp-gateway-catalog` provides
mcp-gateway v0.7.1.

```bash
oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: kuadrant-full-catalog
  namespace: openshift-marketplace
spec:
  sourceType: grpc
  image: quay.io/pstefans/kuadrant-full-catalog:latest
  displayName: Kuadrant (v1.5.0 + v1.5.1 POC)
  publisher: pstefans
---
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: mcp-gateway-catalog
  namespace: openshift-marketplace
spec:
  sourceType: grpc
  image: ghcr.io/kuadrant/mcp-controller-catalog:v0.7.1
  displayName: MCP Gateway (upstream)
  publisher: Kuadrant
EOF

until \
  oc get catalogsource kuadrant-full-catalog -n openshift-marketplace \
    -o jsonpath='{.status.connectionState.lastObservedState}' 2>/dev/null | grep -q READY && \
  oc get catalogsource mcp-gateway-catalog -n openshift-marketplace \
    -o jsonpath='{.status.connectionState.lastObservedState}' 2>/dev/null | grep -q READY; do
  echo "Waiting for catalogs..."; sleep 5
done && echo "All catalogs READY"
```

### 1c. Install kuadrant-operator v1.5.0 and mcp-gateway v0.7.1

Subscribe to kuadrant-operator **first** with a fixed subscription name you control.
This prevents OLM from auto-creating a second subscription when mcp-gateway resolves its
`kuadrant-operator >= 1.4.3` dependency — OLM sees the named subscription already satisfies
it and reuses it. A single controlled subscription is required for a clean in-place upgrade
in Phase 2 (patching `startingCSV` on the same subscription, no CSV deletion needed).

```bash
oc new-project mcp-system
oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
  name: mcp-gateway-group
  namespace: mcp-system
spec:
  upgradeStrategy: Default
---
# Subscribe to kuadrant-operator first with a fixed name.
# mcp-gateway's dependency resolution will reuse this subscription.
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: kuadrant-operator
  namespace: mcp-system
spec:
  channel: stable
  name: kuadrant-operator
  source: kuadrant-full-catalog
  sourceNamespace: openshift-marketplace
  startingCSV: kuadrant-operator.v1.5.0
  installPlanApproval: Automatic
---
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: mcp-gateway
  namespace: mcp-system
spec:
  channel: preview
  name: mcp-gateway
  source: mcp-gateway-catalog
  sourceNamespace: openshift-marketplace
  installPlanApproval: Automatic
EOF

until oc get csv -n mcp-system 2>/dev/null | grep 'kuadrant-operator.v1.5.0' | grep -q Succeeded && \
      oc get csv -n mcp-system 2>/dev/null | grep 'mcp-gateway.v0.7.1' | grep -q Succeeded; do
  echo "Waiting for kuadrant-operator + mcp-gateway..."; sleep 10
done && echo "Both operators ready"
oc get csv -n mcp-system | grep -E 'kuadrant-operator|mcp-gateway'
```

### 1d. Create Gateway

Two listeners are required. `mcp` is the public-facing listener clients connect to.
`mcps` is the internal wildcard listener upstream MCP server HTTPRoutes attach to —
the router hairpins initialize requests through this listener before forwarding tool calls.

```bash
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
    hostname: "placeholder"
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

sleep 15
GATEWAY_HOST=$(oc get gateway mcp-gateway -n istio-system \
  -o jsonpath='{.status.addresses[0].value}')
echo "Gateway: $GATEWAY_HOST"
oc patch gateway mcp-gateway -n istio-system --type=json \
  -p "[{\"op\":\"replace\",\"path\":\"/spec/listeners/0/hostname\",\"value\":\"$GATEWAY_HOST\"}]"
```

### 1e. Create MCPGatewayExtension and upstream server

```bash
GATEWAY_HOST=$(oc get gateway mcp-gateway -n istio-system \
  -o jsonpath='{.status.addresses[0].value}')

# ReferenceGrant allows the cross-namespace reference from mcp-system to istio-system
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
    namespace: mcp-system
  to:
  - group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
---
apiVersion: mcp.kuadrant.io/v1alpha1
kind: MCPGatewayExtension
metadata:
  name: mcp-gateway-extension
  namespace: mcp-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
    namespace: istio-system
    sectionName: mcp
  publicHost: $GATEWAY_HOST
EOF

oc wait mcpgatewayextension mcp-gateway-extension -n mcp-system \
  --for=jsonpath='{.status.conditions[0].reason}'=ValidMCPGatewayExtension \
  --timeout=60s

oc new-project mcp-test
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
    sectionName: mcps
  hostnames:
  - server1.mcp.local
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

oc wait mcpserverregistration server1 -n mcp-test \
  --for=jsonpath='{.status.conditions[0].reason}'=Ready \
  --timeout=60s
```

### 1f. Verify baseline traffic

```bash
GATEWAY_HOST=$(oc get gateway mcp-gateway -n istio-system \
  -o jsonpath='{.status.addresses[0].value}')

curl -s -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" \
  -D /tmp/baseline_headers.txt -o /dev/null \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"baseline","version":"1.0"}}}'
SID=$(grep -i mcp-session-id /tmp/baseline_headers.txt | awk '{print $2}' | tr -d '\r\n')

curl -s -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"server1_greet","arguments":{"name":"baseline"}}}'
# Expected: data: {...,"text":"Hi baseline"}
```

**Baseline confirmed. Now start the monitor in a separate terminal before proceeding.**

---

## Continuous traffic monitor (run in a separate terminal)

Keep this running throughout Stage 2. It polls every 2 seconds and logs any disruption.

```bash
GATEWAY_HOST=$(oc get gateway mcp-gateway -n istio-system \
  -o jsonpath='{.status.addresses[0].value}')

while true; do
  curl -s --max-time 5 -X POST "http://$GATEWAY_HOST:8080/mcp" \
    -H "Content-Type: application/json" \
    -D /tmp/monitor_headers.txt \
    -o /tmp/monitor_init.txt \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"monitor","version":"1.0"}}}' 2>/dev/null
  HTTP_CODE=$(grep '^HTTP' /tmp/monitor_headers.txt | awk '{print $2}' | tr -d '\r')
  SID=$(grep -i mcp-session-id /tmp/monitor_headers.txt | awk '{print $2}' | tr -d '\r\n')
  if [ -z "$SID" ] || [ "$HTTP_CODE" != "200" ]; then
    echo "$(date -u +%H:%M:%S) ERROR: init failed (HTTP $HTTP_CODE)"; sleep 2; continue
  fi
  curl -s --max-time 5 -X POST "http://$GATEWAY_HOST:8080/mcp" \
    -H "Content-Type: application/json" \
    -H "mcp-session-id: $SID" \
    -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"server1_greet","arguments":{"name":"monitor"}}}' \
    -o /tmp/monitor_call.txt 2>/dev/null
  RESULT=$(grep '^data:' /tmp/monitor_call.txt | sed 's/data: //' | python3 -c "
import sys,json
try: print(json.loads(sys.stdin.read())['result']['content'][0]['text'])
except: print('PARSE_ERROR')" 2>/dev/null)
  [ "$RESULT" = "Hi monitor" ] && \
    echo "$(date -u +%H:%M:%S) OK  — $RESULT" || \
    echo "$(date -u +%H:%M:%S) ERROR: '$RESULT'"
  sleep 2
done
```

---

## Stage 2 — Upgrade to kuadrant-operator POC (zero-downtime)

### Phase 0 — Confirm v1 state

```bash
oc get csv -n mcp-system | grep kuadrant-operator
# Expected: kuadrant-operator.v1.5.0   Succeeded

oc get csv -n mcp-system | grep mcp-gateway
# Expected: mcp-gateway.v0.7.1   Succeeded

oc get deployment mcp-gateway-controller -n mcp-system
# Expected: READY 1/1  (mcp-gateway operator's controller)

oc get deployment mcp-gateway -n mcp-system
# Expected: READY 1/1  (broker-router, owned by MCPGatewayExtension CR)

oc get mcpgatewayextension -A
# Expected: mcp-system   mcp-gateway-extension   True
```

### Phase 1 — Remove mcp-gateway OLM subscription

OLM prevents two operators owning the same CRDs simultaneously. Removing the mcp-gateway
subscription relinquishes CRD ownership. The broker-router keeps running — it is owned by
the MCPGatewayExtension CR, not the CSV.

```bash
oc delete subscription mcp-gateway -n mcp-system
oc delete csv mcp-gateway.v0.7.1 -n mcp-system

# Verify: controller gone, broker still running
oc get deployment -n mcp-system
# Expected: only mcp-gateway (broker-router), NOT mcp-gateway-controller

# Monitor should still show OK — traffic unaffected during this gap
```

> **Finalizer note:** Do not delete the MCPGatewayExtension CR during Phase 1. It has
> a finalizer (`mcp.kuadrant.io/finalizer`) that requires a running controller to remove.
> The new kuadrant-operator controller installed in Phase 2 will process it normally.

### Phase 2 — Upgrade kuadrant-operator to POC version via OLM

Both v1.5.0 and v1.5.1 are in the same `kuadrant-full-catalog` channel with a `replaces`
edge. OLM drives upgrades from channel head detection, not from `startingCSV` — so
patching `startingCSV` on an existing subscription does not trigger an upgrade. Instead,
delete the old subscription and CSVs to clear the `@existing` constraint, then create a
new subscription targeting v1.5.1 directly.

```bash
# Delete old kuadrant subscriptions and CSVs to clear @existing
oc delete subscription \
  $(oc get subscription -n mcp-system | grep -E 'kuadrant-operator|authorino|limitador|dns-operator' | awk '{print $1}') \
  -n mcp-system 2>/dev/null
oc delete csv kuadrant-operator.v1.5.0 \
  authorino-operator.v0.25.1 limitador-operator.v0.18.2 dns-operator.v0.17.0 \
  -n mcp-system 2>/dev/null

# Create new subscription targeting v1.5.1
oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: kuadrant-operator
  namespace: mcp-system
spec:
  channel: stable
  name: kuadrant-operator
  source: kuadrant-full-catalog
  sourceNamespace: openshift-marketplace
  startingCSV: kuadrant-operator.v1.5.1
  installPlanApproval: Automatic
  config:
    env:
    - name: MCP_CONTROLLER_IMAGE_REPO
      value: quay.io/pstefans/mcp-controller
    - name: MCP_CONTROLLER_IMAGE_TAG
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

# OLM may reinstall dependency CSVs as @existing — delete them as they appear
until oc get csv kuadrant-operator.v1.5.1 -n mcp-system &>/dev/null; do
  STALE=$(oc get csv -n mcp-system --no-headers 2>/dev/null | \
    grep -v 'mcp-gateway\|kuadrant-operator.v1.5.1' | awk '{print $1}')
  [ -n "$STALE" ] && oc delete csv $STALE -n mcp-system 2>/dev/null && \
    oc delete subscription \
      $(oc get subscription -n mcp-system | grep -E 'authorino|limitador|dns-operator' | awk '{print $1}') \
      -n mcp-system 2>/dev/null || true
  echo "Waiting for v1.5.1 CSV..."; sleep 8
done
oc wait csv/kuadrant-operator.v1.5.1 -n mcp-system \
  --for=jsonpath='{.status.phase}'=Succeeded --timeout=180s

# No Kuadrant CR needed — MCPGatewayReconciler fires on MCPGatewayExtension events.
# The existing MCPGatewayExtension in mcp-system triggers the controller deployment
# automatically when kuadrant-operator v1.5.1 starts (Independent CR pattern).

# Verify new controller running
oc get deployment mcp-gateway-controller -n mcp-system
# Expected: READY 1/1

# Verify existing extension still Ready (broker never restarted)
oc get mcpgatewayextension -A
# Expected: mcp-system   mcp-gateway-extension   True
```

### Phase 3 — Verify no traffic disruption

```bash
GATEWAY_HOST=$(oc get gateway mcp-gateway -n istio-system \
  -o jsonpath='{.status.addresses[0].value}')

curl -s -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" \
  -D /tmp/phase3_headers.txt -o /dev/null \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"post-upgrade","version":"1.0"}}}'
SID=$(grep -i mcp-session-id /tmp/phase3_headers.txt | awk '{print $2}' | tr -d '\r\n')

curl -s -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" \
  -H "mcp-session-id: $SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"server1_greet","arguments":{"name":"post-upgrade"}}}'
# Expected: "Hi post-upgrade" — traffic uninterrupted
```

Check the monitor — it should show only `OK` lines throughout with no errors.

---

## What OLM owns after upgrade

| Resource | v1 (before) | v2 (after) |
|---|---|---|
| mcp CRDs | mcp-gateway v0.7.1 CSV | kuadrant-operator v1.5.1 CSV |
| mcp controller ClusterRole | mcp-gateway v0.7.1 CSV | kuadrant-operator v1.5.1 CSV |
| Controller Deployment | `mcp-system` (mcp-gateway operator) | `mcp-system` (kuadrant-operator) |
| Broker-router Deployment | `mcp-system` (MCPGatewayExtension ownerRef) | `mcp-system` (unchanged) |
| MCPGatewayExtension CR | Created by user | Unchanged |

---

## Rollback

If Phase 3 fails, restore the mcp-gateway operator:

```bash
# Patch the subscription back to v1.5.0 — OLM will downgrade via the replaces chain
oc patch subscription kuadrant-operator -n mcp-system --type=merge -p \
  '{"spec":{"startingCSV":"kuadrant-operator.v1.5.0","config":{"env":[]}}}'

# Reinstall mcp-gateway operator (re-takes CRD ownership and controller)
oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: mcp-gateway
  namespace: mcp-system
spec:
  channel: preview
  name: mcp-gateway
  source: mcp-gateway-catalog
  sourceNamespace: openshift-marketplace
  installPlanApproval: Automatic
EOF
```

Broker-router is unaffected throughout any rollback sequence.

---

## Image compatibility note

The upstream mcp-gateway catalog (`ghcr.io/kuadrant/mcp-controller-catalog:v0.7.1`) deploys
`ghcr.io/kuadrant/mcp-gateway` which uses `./mcp_gateway` as the binary path — matching the
controller's Deployment template. In a downstream (RHCL) deployment both operators use
`registry.redhat.io` images with consistent binary paths; the upgrade procedure is identical
but must be validated as part of downstream release testing.

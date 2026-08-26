# Upgrading from Standalone MCP Gateway to the Kuadrant Operator (OLM)

This guide migrates an OLM-installed MCP Gateway from the **standalone** operator (its own OLM
subscription) to the **consolidated** deployment, where the Kuadrant Operator owns the MCP
Gateway CRDs and runs its controller. The migration is designed to be zero-downtime: the data
plane keeps serving while ownership transfers.

## Who this is for

You installed MCP Gateway (version `<1.0`) via a `Subscription` named `mcp-gateway` (or
equivalent) and now want the Kuadrant Operator to manage it instead. This is the model described
in [RFC 0019](https://github.com/Kuadrant/architecture/pull/189): the Kuadrant Operator runs the
MCP Gateway controller; no `Kuadrant` CR is required for it to start.

If you installed MCP Gateway with Helm rather than OLM, this guide does not apply — see
[Installing and Configuring MCP Gateway](./how-to-install-and-configure.md).

## Prerequisites

- A Kubernetes or OpenShift cluster with OLM (OpenShift includes OLM by default).
- MCP Gateway installed via its own OLM subscription, with at least one `MCPGatewayExtension`
  Ready and traffic flowing.
- The Kuadrant Operator installed via OLM in the same namespace.
- A catalog source providing a Kuadrant Operator version that bundles MCP Gateway. Your
  administrator or the Kuadrant release notes will tell you which version and catalog to use.

> **Note:** Throughout this guide, `mcp-system` is the namespace where the operators and their
> `OperatorGroup` live. Substitute your own namespace if different.

## Why this works without downtime

The MCP Gateway data plane (the broker-router `Deployment`, its `Service`, the `HTTPRoute`, and
the `EnvoyFilter`) is owned by your `MCPGatewayExtension`, not by any operator's
ClusterServiceVersion (CSV). Removing or replacing an operator CSV never touches it, so the Envoy
routing path stays up throughout the migration.

## Step 1: Record the current state

Record current state so you can verify the migration and roll back if needed.

```bash
# The MCP Gateway operator's controller
kubectl get deployment mcp-gateway-controller -n mcp-system

# The broker-router (data plane) — this must keep running throughout
kubectl get deployment mcp-gateway -n mcp-system

# Your extension(s) — must stay Ready
kubectl get mcpgatewayextension -A

# Which CSV currently owns the MCP CRDs
kubectl get crd mcpgatewayextensions.mcp.kuadrant.io \
  -o jsonpath='{.metadata.labels}{"\n"}'
```

The CRD labels include an `operators.coreos.com/<csv>.<namespace>` entry naming the owning CSV.
Before migration this is the standalone `mcp-gateway` CSV.

## Step 2: Remove the standalone MCP Gateway subscription

OLM does not allow two operators to own the same CRDs. Removing the standalone subscription and
its CSV relinquishes ownership of the MCP CRDs so the Kuadrant Operator can take them over.

```bash
kubectl delete subscription mcp-gateway -n mcp-system
kubectl delete csv -n mcp-system -l operators.coreos.com/mcp-gateway.mcp-system
```

Deleting the CSV removes the `mcp-gateway-controller` Deployment but not the broker-router. Confirm the data plane and
your resources survived:

```bash
kubectl get deployment -n mcp-system
# mcp-gateway-controller is gone; mcp-gateway (broker-router) is still 1/1
```

The MCP CRDs and your `MCPGatewayExtension`, `MCPServerRegistration` and `MCPVirtualServer`
resources are untouched — OLM does not delete CRDs when a CSV is removed.

> **Do not delete the `MCPGatewayExtension` during this step.** Its finalizer needs a running
> controller to clear; the Kuadrant Operator's controller (Step 3) handles it once it starts.

> **Note:** No MCP Gateway controller runs until the Kuadrant Operator's controller starts
> (Step 3/4). During this window, create/update/delete operations, finalizers, and status
> changes on `MCPGatewayExtension`, `MCPServerRegistration`, and `MCPVirtualServer` resources
> are not reconciled.

## Step 3: Move the Kuadrant Operator to the version that bundles MCP Gateway

Upgrade the Kuadrant Operator to the version that includes the MCP Gateway controller. How this
happens depends on how your catalog ships it:

- **Same channel, newer version (typical production upgrade):** if your subscription's channel
  already offers the newer version, OLM upgrades automatically (for `installPlanApproval:
  Automatic`) or presents an InstallPlan to approve.

- **Explicit version target:** delete the existing Kuadrant subscription and CSV, then recreate
  the subscription with `startingCSV` set to the target version:

```bash
kubectl apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: kuadrant-operator
  namespace: mcp-system
spec:
  channel: <channel>
  name: kuadrant-operator
  source: <catalog-source>
  sourceNamespace: <catalog-namespace>
  startingCSV: kuadrant-operator.<target-version>
  installPlanApproval: Automatic
EOF
```

> **Note:** `<catalog-namespace>` is the namespace of your catalog source: `openshift-marketplace`
> on OpenShift, `olm` on most other OLM installs.

Wait for the new CSV to succeed:

```bash
kubectl wait csv/kuadrant-operator.<target-version> -n mcp-system \
  --for=jsonpath='{.status.phase}'=Succeeded --timeout=5m
```

## Step 4: Verify the Kuadrant Operator has taken over

The Kuadrant Operator now owns the MCP CRDs and runs the controller; no `Kuadrant` CR is required
for it to start. (A `Kuadrant` CR is only needed later for `AuthPolicy` or `RateLimitPolicy`.)

```bash
# CRD ownership has transferred to the Kuadrant Operator CSV
kubectl get crd mcpgatewayextensions.mcp.kuadrant.io \
  -o jsonpath='{.metadata.labels}{"\n"}'
# The operators.coreos.com/... label now names the kuadrant-operator CSV

# The controller is running again, now deployed by the Kuadrant Operator
kubectl get deployment mcp-gateway-controller -n mcp-system
# READY 1/1

# Your extension is still Ready
kubectl get mcpgatewayextension -A
# READY True
```

> **Note:** If the new release pins a newer broker-router image, the controller rolls the pods
> behind the stable `Service` as a rolling update: new pods become Ready before old ones are
> removed, so requests continue to be served. Restarting pods is expected; the `Deployment`,
> `Service`, `HTTPRoute` and `EnvoyFilter` are never deleted.

## Step 5: Confirm traffic is being served

Send a request through the gateway and confirm it succeeds.

```bash
GATEWAY_HOST=<your-gateway-hostname>

curl -s -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" -D /tmp/hdr.txt -o /dev/null \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"post-upgrade","version":"1.0"}}}'
SID=$(grep -i mcp-session-id /tmp/hdr.txt | awk '{print $2}' | tr -d '\r\n')
curl -s -X POST "http://$GATEWAY_HOST:8080/mcp" \
  -H "Content-Type: application/json" -H "mcp-session-id: $SID" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"<your-tool>","arguments":{}}}'
```

> **Note:** This bare `curl` assumes an unauthenticated listener. If you have an `AuthPolicy` on
> the route, verify with your normal authenticated client instead. A `401`/`403` confirms only
> that the gateway and authentication layer are reachable — the request was rejected before
> broker-router. To confirm broker-router is serving traffic, use an authenticated client and
> check for a successful response.

## Rollback

If the migration fails, return the Kuadrant Operator to its previous version (via the same
mechanism as Step 3), then recreate the MCP Gateway subscription:

```bash
kubectl apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: mcp-gateway
  namespace: mcp-system
spec:
  channel: <mcp-gateway-channel>
  name: mcp-gateway
  source: <mcp-gateway-catalog>
  sourceNamespace: <catalog-namespace>
  installPlanApproval: Automatic
EOF
```

The broker-router is unaffected by rollback — it is owned by your `MCPGatewayExtension`, not by
either operator's CSV.

## Next Steps

- [Installing MCP Gateway via OLM (Kuadrant Operator)](./olm-install.md)
- [Register MCP Servers](./register-mcp-servers.md)
- [Authentication](./authentication.md)

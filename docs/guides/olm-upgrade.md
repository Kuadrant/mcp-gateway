# Upgrading from Standalone MCP Gateway to the Kuadrant Operator (OLM)

This guide covers migrating an OLM-installed MCP Gateway from the **standalone** operator
(its own OLM subscription) to the **consolidated** deployment, where the Kuadrant Operator
owns the MCP Gateway CRDs and runs its controller.

The migration is zero-downtime: MCP traffic is not interrupted while ownership transfers.

## Who this is for

You installed MCP Gateway via its own OLM subscription (a `Subscription` named `mcp-gateway`,
or equivalent, that installed the `mcp-gateway` operator) and you now want the Kuadrant
Operator to manage MCP Gateway instead. This is the model described in
[RFC 0019](https://github.com/Kuadrant/architecture/pull/189): the Kuadrant Operator embeds
the MCP Gateway controller and deploys it on startup.

If you installed MCP Gateway with Helm rather than OLM, this guide does not apply — see
[Installing and Configuring MCP Gateway](./how-to-install-and-configure.md).

## Prerequisites

- An OpenShift cluster with OLM (OpenShift includes OLM by default).
- MCP Gateway installed via its own OLM subscription, with at least one `MCPGatewayExtension`
  in a Ready state and traffic flowing.
- The Kuadrant Operator installed via OLM in the same namespace.
- A catalog source that provides a Kuadrant Operator version which bundles MCP Gateway. Your
  cluster administrator or the Kuadrant release notes will tell you which version and catalog
  to use.

> **Note:** Throughout this guide, `mcp-system` is the namespace where the operators and their
> `OperatorGroup` live. Substitute your own namespace if different.

## Why this works without downtime

The MCP Gateway data plane — the broker-router `Deployment`, its `Service`, the `HTTPRoute`,
and the `EnvoyFilter` — is owned by your `MCPGatewayExtension` custom resource, not by the
operator's ClusterServiceVersion (CSV). Removing or replacing an operator CSV therefore never
deletes these resources. The Envoy routing path stays in place for the entire migration, so
in-flight and new requests continue to be served.

## Step 1: Record the current state

Capture what you have before changing anything, so you can confirm the migration and roll back
if needed.

```bash
# The MCP Gateway operator's controller
oc get deployment mcp-gateway-controller -n mcp-system

# The broker-router (data plane) — this must keep running throughout
oc get deployment mcp-gateway -n mcp-system

# Your extension(s) — must stay Ready
oc get mcpgatewayextension -A

# Which CSV currently owns the MCP CRDs
oc get crd mcpgatewayextensions.mcp.kuadrant.io \
  -o jsonpath='{.metadata.labels}{"\n"}'
```

The CRD labels include an `operators.coreos.com/<csv>.<namespace>` entry naming the CSV that
currently owns the CRD. Before migration this names the standalone `mcp-gateway` CSV.

## Step 2: Start a traffic monitor (recommended)

In a separate terminal, poll your gateway continuously so you can see for yourself that
traffic is uninterrupted. Replace `GATEWAY_HOST`, the tool name, and the arguments with values
that match your deployment.

```bash
GATEWAY_HOST=<your-gateway-hostname>

while true; do
  curl -s --max-time 5 -X POST "http://$GATEWAY_HOST:8080/mcp" \
    -H "Content-Type: application/json" -D /tmp/hdr.txt -o /dev/null \
    -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"monitor","version":"1.0"}}}'
  SID=$(grep -i mcp-session-id /tmp/hdr.txt | awk '{print $2}' | tr -d '\r\n')
  curl -s --max-time 5 -X POST "http://$GATEWAY_HOST:8080/mcp" \
    -H "Content-Type: application/json" -H "mcp-session-id: $SID" \
    -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"<your-tool>","arguments":{}}}'
  echo " — $(date -u +%H:%M:%S)"
  sleep 2
done
```

Leave this running until the migration is complete.

## Step 3: Remove the standalone MCP Gateway subscription

OLM does not allow two operators to own the same CRDs at once. Removing the standalone MCP
Gateway subscription and its CSV relinquishes ownership of the MCP CRDs so the Kuadrant
Operator can take them over.

```bash
oc delete subscription mcp-gateway -n mcp-system
oc delete csv -n mcp-system -l operators.coreos.com/mcp-gateway.mcp-system
```

> **Note:** Deleting the CSV removes the `mcp-gateway-controller` Deployment, but **not** the
> broker-router. Confirm the data plane is still running:

```bash
oc get deployment -n mcp-system
# mcp-gateway-controller is gone; mcp-gateway (broker-router) is still 1/1
```

The MCP CRDs remain on the cluster — OLM does not delete CRDs when a CSV is removed. Your
`MCPGatewayExtension` and `MCPServerRegistration` resources are untouched, and the monitor from
Step 2 continues to report success.

> **Do not delete the `MCPGatewayExtension` during this step.** It carries a finalizer that
> requires a running controller to clear. The Kuadrant Operator's controller (Step 4) processes
> it normally once it starts.

## Step 4: Move the Kuadrant Operator to the version that bundles MCP Gateway

Upgrade the Kuadrant Operator to the version that includes the MCP Gateway controller. How this
happens depends on how your catalog ships that version:

- **Same channel, newer version (typical production upgrade):** if your existing subscription's
  channel already offers the newer Kuadrant Operator, OLM detects it and generates the upgrade
  automatically (for `installPlanApproval: Automatic`) or presents an InstallPlan to approve.
  No manual subscription change is needed.

- **Explicit version target:** if you need to point at a specific version, delete the existing
  Kuadrant subscription and CSV, then recreate the subscription with `startingCSV` set to the
  target version. Use the catalog and channel provided by your administrator or the release
  notes:

```bash
oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: kuadrant-operator
  namespace: mcp-system
spec:
  channel: <channel>
  name: kuadrant-operator
  source: <catalog-source>
  sourceNamespace: openshift-marketplace
  startingCSV: kuadrant-operator.<target-version>
  installPlanApproval: Automatic
EOF
```

Wait for the new CSV to succeed:

```bash
oc wait csv/kuadrant-operator.<target-version> -n mcp-system \
  --for=jsonpath='{.status.phase}'=Succeeded --timeout=5m
```

## Step 5: Verify the Kuadrant Operator has taken over

The Kuadrant Operator now owns the MCP CRDs and runs the MCP Gateway controller. No `Kuadrant`
custom resource is required for the controller to start — it deploys as soon as the operator
starts. (A `Kuadrant` CR is only needed later if you want to apply `AuthPolicy` or
`RateLimitPolicy`.)

```bash
# CRD ownership has transferred to the Kuadrant Operator CSV
oc get crd mcpgatewayextensions.mcp.kuadrant.io \
  -o jsonpath='{.metadata.labels}{"\n"}'
# The operators.coreos.com/... label now names the kuadrant-operator CSV

# The controller is running again, now deployed by the Kuadrant Operator
oc get deployment mcp-gateway-controller -n mcp-system
# READY 1/1

# Your extension is still Ready
oc get mcpgatewayextension -A
# READY True
```

> **Note:** When the new controller reconciles your `MCPGatewayExtension`, it may roll the
> broker-router pods (a rolling update behind the stable `Service`). New pods become Ready
> before old ones are removed, so no requests are dropped — the broker-router `Deployment`,
> `Service`, `HTTPRoute` and `EnvoyFilter` are never deleted.

## Step 6: Confirm traffic was uninterrupted

Send a request and check the monitor from Step 2 — it should show no failures across the entire
migration.

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

You can now stop the monitor.

## Rollback

If the migration fails, restore the standalone MCP Gateway operator. Return the Kuadrant
Operator to its previous version (via the same subscription mechanism you used in Step 4), then
recreate the MCP Gateway subscription:

```bash
oc apply -f - <<EOF
apiVersion: operators.coreos.com/v1alpha1
kind: Subscription
metadata:
  name: mcp-gateway
  namespace: mcp-system
spec:
  channel: <mcp-gateway-channel>
  name: mcp-gateway
  source: <mcp-gateway-catalog>
  sourceNamespace: openshift-marketplace
  installPlanApproval: Automatic
EOF
```

The broker-router is unaffected by any rollback — it is owned by your `MCPGatewayExtension`, not
by either operator's CSV.

## Next Steps

- [Register MCP Servers](./register-mcp-servers.md)
- [Authentication](./authentication.md)
- [Authorization](./authorization.md)

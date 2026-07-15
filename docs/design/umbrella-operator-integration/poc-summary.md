# POC Summary: mcp-gateway as a kuadrant-operator Managed Component

## What We Built and Verified

This POC proves that mcp-gateway can be integrated into kuadrant-operator as a managed
component using the same Helm-based deployment pattern established for Authorino, Limitador,
and DNS-operator. We tested two scenarios end-to-end on OpenShift 4.22 with OLM:

1. **Fresh OLM install** — kuadrant-operator deploys and manages the mcp-gateway controller
2. **Zero-downtime OLM upgrade** — upgrading from standalone mcp-gateway + kuadrant-operator v1.5.0
   to kuadrant-operator v1.5.1-poc which takes over management

Both scenarios confirmed live MCP traffic (`server1_greet` tool calls) continued
uninterrupted throughout.

---

## What Gets Deployed and By Whom

### OLM installs from the kuadrant-operator bundle (automatic)

| Resource | Who creates it |
|---|---|
| `mcpgatewayextensions.mcp.kuadrant.io` CRD | OLM (from bundle manifests) |
| `mcpserverregistrations.mcp.kuadrant.io` CRD | OLM (from bundle manifests) |
| `mcpvirtualservers.mcp.kuadrant.io` CRD | OLM (from bundle manifests) |
| `kuadrant-operator-mcp-gateway-controller` ClusterRole | OLM (from bundle manifests) |
| `kuadrant-operator-wasm` Service (port 8082) | OLM (from bundle manifests) |
| kuadrant-operator Deployment | OLM (from CSV) |

### kuadrant-operator deploys at runtime via Helm SSA

| Resource | Namespace | Who creates it |
|---|---|---|
| `mcp-gateway-controller` ServiceAccount | operator namespace | kuadrant-operator (Helm chart SSA) |
| `mcp-gateway-controller` ClusterRoleBinding | cluster | kuadrant-operator (Helm chart SSA) |
| `mcp-gateway-controller` Deployment | operator namespace | kuadrant-operator (Helm chart SSA) |

Trigger: MCPGatewayExtension CR exists in any namespace (Independent CR pattern —
no Kuadrant CR needed).

### mcp-gateway controller deploys per MCPGatewayExtension

| Resource | Namespace | Who creates it |
|---|---|---|
| `mcp-gateway` broker-router Deployment | MCPGatewayExtension namespace | mcp-gateway controller |
| `mcp-gateway` Service (8080/50051) | MCPGatewayExtension namespace | mcp-gateway controller |
| `mcp-gateway` ServiceAccount | MCPGatewayExtension namespace | mcp-gateway controller |
| `mcp-gateway-route` HTTPRoute | MCPGatewayExtension namespace | mcp-gateway controller |
| `mcp-gateway-config` Secret | MCPGatewayExtension namespace | mcp-gateway controller |
| `mcp-gateway-session-signing-key` Secret | MCPGatewayExtension namespace | mcp-gateway controller |
| EnvoyFilter | Gateway namespace (istio-system) | mcp-gateway controller |
| Gateway listener status | Gateway namespace | mcp-gateway controller |

**The broker-router is owned by the MCPGatewayExtension CR via ownerRef** — not by the
operator or controller Deployment. This is the key property that enables zero-downtime
upgrades: the broker-router keeps serving regardless of operator or controller restarts.

### mcp-gateway controller manages per MCPServerRegistration

| Resource | Who creates it |
|---|---|
| Server entry in `mcp-gateway-config` Secret | mcp-gateway controller |
| HTTPRoute status conditions | mcp-gateway controller |

---

## Upgrade Test: How We Verified Zero-Downtime

### The upgrade scenario

- **Before**: kuadrant-operator v1.5.0 + mcp-gateway v0.7.1 both installed via OLM, both
  from separate operator bundles, live MCP traffic flowing
- **After**: kuadrant-operator v1.5.1-poc installed via OLM from a single combined catalog,
  takes over mcp CRD and controller management, mcp-gateway operator removed

### How we built the upgrade path

Because this is a POC against a fork (not the upstream kuadrant catalog), we built our own
OLM catalog image containing both versions in a single channel:

```
quay.io/pstefans/kuadrant-full-catalog:latest
  stable channel:
    kuadrant-operator.v1.5.0  (upstream bundle, all dependencies)
    kuadrant-operator.v1.5.1  (POC bundle, replaces: v1.5.0)
```

The v1.5.1 bundle is identical to upstream except:
- Version field: `1.5.1`
- Adds `replaces: kuadrant-operator.v1.5.0` in both the CSV and the FBC channel entry
- Adds mcp CRDs to the `owned:` section of the CSV
- Adds mcp-gateway ClusterRole to the bundle manifests

The catalog image is built with `opm render` of the upstream v1.5.0 catalog, then the
v1.5.1 bundle is appended and the `stable` channel entry is patched to include both versions.

### How we verified the connection stayed alive

A monitor script ran in a **separate terminal throughout the entire upgrade**. Every 2 seconds it:

1. Opened a fresh MCP session through the Istio Gateway (`POST /mcp initialize`)
2. Checked the response for a valid `mcp-session-id` header and HTTP 200
3. Called `server1_greet("monitor")` on that session
4. Logged `OK — Hi monitor` on success or `ERROR: <reason>` on any failure

```
11:21:01 OK  — Hi monitor    ← baseline, both operators installed
11:21:03 OK  — Hi monitor
11:21:05 OK  — Hi monitor    ← Phase 1: mcp-gateway subscription deleted
11:21:07 OK  — Hi monitor    ← controller gone, broker-router still serving
11:21:09 OK  — Hi monitor
11:21:11 OK  — Hi monitor    ← Phase 2: kuadrant-operator CSVs deleted
11:21:13 OK  — Hi monitor    ← OLM resolving v1.5.1
11:21:15 OK  — Hi monitor    ← v1.5.1 InstallPlan approved
11:21:17 OK  — Hi monitor
11:21:19 OK  — Hi monitor    ← v1.5.1 Succeeded, new controller deploying
11:21:21 OK  — Hi monitor    ← new controller running
11:21:23 OK  — Hi monitor    ← Phase 3: post-upgrade tool call verified
```

The monitor showed **no errors at any point**. The broker-router continued serving without
interruption because it is owned by the MCPGatewayExtension CR (not by any operator CSV),
so operator and controller restarts have no effect on live traffic.

Final verification after upgrade: `server1_greet("post-upgrade")` → `"Hi post-upgrade"`.

**Broker-router pod replacement is expected.** When kuadrant-operator v1.5.1 starts and
reconciles the existing MCPGatewayExtension, it SSA-applies the broker-router Deployment.
If any field differs from what v1.5.1 expects (image tag, env vars, labels), Kubernetes
performs a rolling update. The default `RollingUpdate` strategy (`maxUnavailable: 0`)
ensures the old pod continues serving until the new one is ready — no traffic gap. The
monitor confirms this: `OK` lines throughout the pod replacement.

### The upgrade phases

**Phase 1 — Remove mcp-gateway operator:**
Delete the mcp-gateway subscription and CSV. This relinquishes CRD ownership. The
broker-router keeps running because it is owned by the MCPGatewayExtension CR, not the CSV.

**Phase 2 — Upgrade kuadrant-operator:**
Delete old subscriptions and CSVs to clear the `@existing` constraint (OLM generates this
constraint dynamically from all CSVs present in the namespace — the only way to clear it is
deletion). Create a new subscription targeting v1.5.1 directly. OLM resolves it via the
`replaces` edge, generates an upgrade InstallPlan, and the new kuadrant-operator starts —
detecting the existing MCPGatewayExtension and deploying the mcp-gateway controller without
touching the broker-router.

**Phase 3 — Verify:**
`server1_greet("post-upgrade")` → `"Hi post-upgrade"` — traffic uninterrupted.

### Workarounds required and why they won't be needed in production

**1. `@existing` constraint / CSV deletion loop**

OLM tracks which CSVs are installed in a namespace and generates `@existing` constraints
at resolution time. When upgrading from a CSV installed by one subscription to a CSV from
a different subscription, OLM's resolver sees both as competing and blocks the upgrade.

Workaround: delete the old CSVs before creating the upgrade subscription. Since OLM
re-creates auto-generated dependency subscriptions (for authorino, limitador, etc.), a loop
was needed to delete them as fast as OLM re-creates them.

**Why not needed in production:** In production there is a single catalog. The user has one
subscription that was always pointing at the same catalog. OLM handles in-place upgrades
within a single subscription natively via the `replaces` chain — no CSV deletion, no loop.
The `@existing` problem only arises when switching between subscriptions or catalogs.

**2. Dependency pre-install workaround (authorino/limitador/dns-operator)**

OCP's built-in `community-operators` catalog provides older versions of kuadrant's
dependencies. When kuadrant v1.5.0 needed authorino v0.25.1, OLM found an older version
in `community-operators` and couldn't resolve.

Workaround: pre-install dependencies from `kuadrant-full-catalog` before subscribing to
kuadrant-operator, pinning the correct versions.

**Why not needed in production:** Red Hat ships all kuadrant components in a coordinated
catalog. The dependency versions are aligned and there's no competing community catalog
providing older versions. The combined catalog (`kuadrant-full-catalog`) already handles
this correctly in the POC for the upgrade scenario.

**3. Authorino service name mismatch**

The Helm-deployed Authorino creates a service named `authorino-auth` but the existing
kuadrant-operator code expected `authorino-authorino-authorization` (the old operator-based
naming convention).

Workaround: created an ExternalName service alias.

**Why not needed in production:** `auth_workflow_helpers.go` was fixed to use the Helm
chart service name `<name>-auth`. This fix is in the feature branch and will be part of
the production release.

**4. AuthConfig label selector**

Helm-deployed Authorino watches for `authorino.kuadrant.io/managed-by=authorino` but
kuadrant-operator was only setting `kuadrant.io/managed=true`.

Workaround: manually added the Authorino label to AuthConfigs.

**Why not needed in production:** `AuthObjectLabels()` was fixed to set both labels.
This fix is in the feature branch.

---

## Outstanding Decisions for Production

### 1. CRD sync mechanism between mcp-gateway and kuadrant-operator

**Problem:** The mcp CRDs (`mcpgatewayextensions`, `mcpserverregistrations`,
`mcpvirtualservers`) live in the mcp-gateway repo but must be bundled into the
kuadrant-operator OLM bundle. Currently they are manually copied.

**Options:**
- `make sync-mcp-crds` target that fetches from a pinned mcp-gateway release tag and
  updates `bundle/manifests/mcp.kuadrant.io_*.yaml` atomically
- CI check that diffs the committed files against a fresh fetch and fails if they diverge
- Git submodule or subtree for the mcp-gateway `api/` directory

**Decision needed:** who owns the sync process and when does it run (on kuadrant release,
on mcp-gateway release, or both)?

### 2. mcp-gateway ClusterRole sync

Same problem as CRDs — the `kuadrant-operator-mcp-gateway-controller` ClusterRole in
`bundle/manifests/` is a copy of the mcp-gateway chart's RBAC. If the mcp-gateway
controller gains new permissions (e.g. for a new CRD), the bundle ClusterRole must be
updated too. The sync target above should cover this.

### 3. Downstream image compatibility

The upstream catalog deploys upstream images (`ghcr.io/kuadrant/mcp-gateway`,
`ghcr.io/kuadrant/mcp-controller`) which use `./mcp_gateway` as the binary path. The
downstream RHEL images (`registry.redhat.io/rhcl-tech-preview/`) use a different binary path.

**Decision needed:** the kuadrant-operator Helm chart's controller Deployment template must
use the correct binary path for whichever image it deploys. In production both operators will
use downstream images so the paths will be consistent — but this must be explicitly verified
and tested as part of downstream release validation.

---

## Repos and Branches

| Repo | Branch | Description |
|---|---|---|
| `github.com/Kuadrant/mcp-gateway` | `feature/umbrella-operator-integration` | mcp-gateway controller changes |
| `github.com/Patryk-Stefanski/kuadrant-operator` | `feature/mcp-gateway-umbrella-poc` | kuadrant-operator integration |

### Key images (all `quay.io/pstefans/`, public)

| Image | Content |
|---|---|
| `kuadrant-operator:latest` | POC kuadrant-operator (amd64) |
| `kuadrant-operator-bundle:mcp-poc` | OLM bundle (v0.0.0 / v1.5.1) |
| `kuadrant-full-catalog:latest` | Combined FBC catalog (v1.5.0 + v1.5.1 upgrade path) |
| `mcp-controller:latest` | mcp-gateway controller (amd64) |
| `mcp-gateway:latest` | mcp-gateway broker-router (amd64) |

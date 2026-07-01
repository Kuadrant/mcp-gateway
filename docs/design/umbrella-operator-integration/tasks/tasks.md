# Implementation Plan: Kuadrant Operator Integration

## Existing Code Analysis

The following artifacts already exist and support the integration:

- **Helm chart** at `charts/mcp-gateway/` with per-component templates, CRDs in `crds/`, and configurable values
- **`controller.enabled`** value in `charts/mcp-gateway/values.yaml` — gates the controller Deployment template. The kuadrant-operator renders the chart per-namespace with `controller.enabled: false` to skip the controller.
- **`RELATED_IMAGE_ROUTER_BROKER` env var** in `cmd/main.go:125` — controller reads the broker-router image from this env var, falling back to `DefaultBrokerRouterImage`. Only needed in standalone mode.
- **Three independent reconcilers** in `cmd/main.go:115-154`:
  - `MCPReconciler` (MCPServerRegistration) — depends on `MCPGatewayExtensionValidator` for finding valid extensions, uses field indexes from MCPGatewayExtensionReconciler
  - `MCPGatewayExtensionReconciler` — creates broker-router Deployment, Service, ServiceAccount, HTTPRoutes, EnvoyFilter, Secrets
  - `MCPVirtualServerReconciler` — independent, no MCPGatewayExtension dependency
- **Field indexes** set up in `MCPGatewayExtensionReconciler.SetupWithManager()` at `internal/controller/mcpgatewayextension_controller.go:907-912` — `setupIndexExtensionToGateway` and `setupIndexExtensionToReferenceGrant`. These are used by `MCPGatewayExtensionValidator.FindValidMCPGatewayExtsForGateway()` which the MCPReconciler depends on.
- **Kuadrant CR** already has `spec.components` with `DeveloperPortal` as a precedent for optional components (`api/v1beta1/kuadrant_types.go`).
- **Umbrella-operator POC** at [Kuadrant/umbrella-operator mcp-gateway-poc](https://github.com/Kuadrant/umbrella-operator/tree/mcp-gateway-poc) — reference implementation for Helm rendering (`helm.go`), value mapping (`buildValues`), server-side apply (`applyManifest`), and migration (`migration.go`). These patterns will be ported into the kuadrant-operator.

## Task 1: Add Controller Headless Mode

**Repo:** mcp-gateway

Add `DISABLE_EXTENSION_RECONCILER` env var to the controller. When set to `true`, skip registering the `MCPGatewayExtensionReconciler`. The MCPReconciler and MCPVirtualServerReconciler continue to run.

**Files:**
- `cmd/main.go` — read env var, conditionally skip MCPGatewayExtensionReconciler registration (lines 135-145). Call `SetupRequiredIndexes()` unconditionally before reconciler registration.
- `internal/controller/mcpgatewayextension_controller.go` — extract `setupIndexExtensionToGateway` and `setupIndexExtensionToReferenceGrant` into an exported `SetupRequiredIndexes(ctx, indexer)` function. Modify `SetupWithManager` to call it (avoiding duplicate registration).
- `charts/mcp-gateway/templates/deployment-controller.yaml` — add `DISABLE_EXTENSION_RECONCILER` env var gated by `controller.disableExtensionReconciler`
- `charts/mcp-gateway/values.yaml` — add `controller.disableExtensionReconciler: false`

**Acceptance criteria:**
- [ ] `DISABLE_EXTENSION_RECONCILER=true` prevents MCPGatewayExtensionReconciler from registering
- [ ] MCPReconciler still works in headless mode (MCPServerRegistration -> config Secret)
- [ ] MCPVirtualServerReconciler still works in headless mode
- [ ] Field indexes are registered unconditionally (MCPReconciler can find valid extensions)
- [ ] Default behavior (env var unset or `false`) is unchanged — all three reconcilers run
- [ ] Unit tests cover both modes
- [ ] `make lint` and `make test-unit` pass

**Verification:**
```bash
make lint
make test-unit
```

## Task 2: Add NetworkPolicy Templates to Helm Chart

**Repo:** mcp-gateway

Add default NetworkPolicy resources for broker-router and controller pods. Ref: [CONNLINK-1115](https://redhat.atlassian.net/browse/CONNLINK-1115).

NetworkPolicy is namespace-scoped — each namespace with a broker-router gets its own policy. The template is written once; the kuadrant-operator's per-namespace rendering stamps it into each namespace automatically.

**Files:**
- `charts/mcp-gateway/templates/networkpolicy-broker-router.yaml` (new) — ingress: 8080/TCP (HTTP), 50051/TCP (gRPC ext_proc). Egress: all (upstream MCP servers, gateway hairpin, optional Redis).
- `charts/mcp-gateway/templates/networkpolicy-controller.yaml` (new) — ingress: 8082/TCP (Prometheus metrics). Egress: 443/TCP (kube-apiserver), 53/TCP+UDP (DNS).
- `charts/mcp-gateway/values.yaml` — add `networkPolicy.enabled: false`

**Acceptance criteria:**
- [ ] `networkPolicy.enabled: true` renders both NetworkPolicy resources
- [ ] `networkPolicy.enabled: false` (default) renders no NetworkPolicy resources
- [ ] Broker-router policy allows ingress on ports 8080 and 50051
- [ ] Broker-router policy allows all egress
- [ ] Controller policy allows ingress on port 8082 (metrics)
- [ ] Controller policy allows egress to kube-apiserver and DNS
- [ ] `helm template` renders correctly with policies enabled and disabled
- [ ] `make lint` passes

**Verification:**
```bash
helm template mcp-gateway charts/mcp-gateway --set networkPolicy.enabled=true | grep -A 30 'kind: NetworkPolicy'
helm template mcp-gateway charts/mcp-gateway --set networkPolicy.enabled=false | grep 'NetworkPolicy'
make lint
```

## Task 3: Fix Chart Versioning

**Repo:** mcp-gateway

**Files:**
- `charts/mcp-gateway/Chart.yaml`

**Acceptance criteria:**
- [ ] `version` field tracks the mcp-gateway release version (e.g., `0.7.1`)
- [ ] `appVersion` field matches the release version (not `"latest"`)

**Verification:**
```bash
grep -E 'version:|appVersion:' charts/mcp-gateway/Chart.yaml
```

## Task 4: Remove OLM Dependency Declaration

**Repo:** mcp-gateway

**Files:**
- `bundle/metadata/dependencies.yaml` (delete this file)

**Acceptance criteria:**
- [ ] `bundle/metadata/dependencies.yaml` is deleted
- [ ] `make bundle` succeeds
- [ ] `make check` passes

**Verification:**
```bash
test ! -f bundle/metadata/dependencies.yaml && echo "deleted"
make bundle
make check
```

## Task 5: Publish Helm Chart to kuadrant.io/helm-charts

**Repo:** mcp-gateway + helm-charts

mcp-gateway already publishes to GHCR OCI registry via `.github/workflows/helm-release.yaml`. This task adds publishing to the Kuadrant shared Helm repository.

**Repositories:**
- [Kuadrant/helm-charts](https://github.com/Kuadrant/helm-charts) — PR by mcp-gateway maintainers

**Files:**
- `.github/workflows/helm-release.yaml` — extend to publish to `kuadrant.io/helm-charts/`
- PR to [Kuadrant/helm-charts](https://github.com/Kuadrant/helm-charts)

**Acceptance criteria:**
- [ ] mcp-gateway chart `.tgz` is published to `https://kuadrant.io/helm-charts/`
- [ ] `index.yaml` in the helm-charts repo includes the mcp-gateway entry
- [ ] Chart is installable via `helm install mcp-gateway kuadrant/mcp-gateway`
- [ ] Release workflow publishes to both GHCR OCI registry (existing) and `kuadrant.io/helm-charts/` (new)

**Verification:**
```bash
helm repo add kuadrant https://kuadrant.io/helm-charts/
helm search repo kuadrant/mcp-gateway
```

## Task 6: POC in kuadrant-operator

**Repo:** kuadrant-operator (new branch)

**Prerequisites:** Task 1 (headless mode)

Create a POC branch on kuadrant-operator that adds mcp-gateway as a managed component. Port the Helm rendering and migration patterns from the [umbrella-operator mcp-gateway-poc](https://github.com/Kuadrant/umbrella-operator/tree/mcp-gateway-poc/internal/controller) reference implementation.

**Implementation:**
- Add `spec.components.mcpGateway` to the Kuadrant CR following the `DeveloperPortal` pattern in `api/v1beta1/kuadrant_types.go`
- Add MCPGatewayExtension controller that renders the mcp-gateway Helm chart per-namespace (port `helm.go`, `buildValues()` from umbrella-operator POC)
- Apply rendered manifests via server-side apply with field ownership (port `applyManifest()`)
- Deploy mcp-gateway-controller as a singleton in headless mode when `mcpGateway.enabled: true`
- Set `controller.enabled: false` in chart rendering (unlike the POC which sets `true`)
- Port migration logic from umbrella-operator POC's `migration.go`

**Acceptance criteria:**
- [ ] Kuadrant CR with `spec.components.mcpGateway.enabled: true` deploys headless mcp-gateway-controller
- [ ] Creating MCPGatewayExtension CR in a namespace triggers chart rendering
- [ ] Broker-router Deployment created in the MCPGatewayExtension namespace
- [ ] MCPServerRegistration config writing works via headless controller
- [ ] End-to-end: register MCP server → tools discoverable via broker

**Re-evaluate if extensions SDK lands:** if kuadrant-operator [#1609](https://github.com/Kuadrant/kuadrant-operator/issues/1609)/[#1612](https://github.com/Kuadrant/kuadrant-operator/issues/1612) defines a registration mechanism, this task should be updated to include any registration CR the kuadrant-operator needs.

**Verification:**
```bash
kubectl get deployment -n kuadrant-system | grep mcp-gateway-controller
kubectl apply -f mcpgatewayextension.yaml -n team-a
kubectl get deployment -n team-a mcp-gateway
```

## Task 7: Full Integration Validation

**Prerequisites:** Tasks 1-5 complete + Task 6 POC

**Acceptance criteria:**
- [ ] Headless mode controller deployed by kuadrant-operator in `kuadrant-system`
- [ ] kuadrant-operator renders chart per-namespace with `controller.enabled: false` using the published chart
- [ ] Broker-router Deployment created in the MCPGatewayExtension namespace
- [ ] MCPServerRegistration config writing works via headless controller
- [ ] NetworkPolicies applied when `networkPolicy.enabled: true`
- [ ] Migration from standalone to kuadrant-operator-managed preserves existing broker-routers

**Verification:**
```bash
kubectl get deployment -n kuadrant-system | grep mcp-gateway-controller
kubectl apply -f mcpgatewayextension.yaml -n team-a
kubectl get deployment -n team-a mcp-gateway
kubectl get networkpolicy -n team-a
```

## Task 8: Document Migration Guide

**Repo:** mcp-gateway

**Files:**
- New guide: `docs/guides/migrate-to-kuadrant-operator.md`
- Update guide index: `docs/guides/README.md`

**Acceptance criteria:**
- [ ] Step-by-step migration instructions from standalone OLM to kuadrant-operator-managed
- [ ] Documents the control plane gap and its impact
- [ ] Confirms existing MCPGatewayExtensions and broker-routers are preserved
- [ ] Includes verification commands at each step
- [ ] Added to `docs/guides/README.md` guide index

## Dependencies Between Tasks

```
Task 1 (Headless mode) ──→ Task 6 (POC in kuadrant-operator)
                      ──┐
Task 2 (NetworkPolicy) ──┤
Task 3 (Chart version) ──┼──→ Task 5 (Publish chart) ──→ Task 7 (Full validation)
Task 4 (OLM deps)     ───┘

Task 8 (Migration guide) — depends on Task 7
```

Tasks 1-4 can proceed in parallel. Task 6 (POC) only needs Task 1 — it can start as soon as headless mode is implemented, without waiting for network policies, chart publishing, or OLM changes. Task 5 requires Tasks 1-4. Task 7 requires Task 5 + Task 6. Task 8 depends on validated integration.

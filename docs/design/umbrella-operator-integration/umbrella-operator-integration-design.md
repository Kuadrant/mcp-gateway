# Kuadrant Operator Integration

## Problem

OLMv1 removes automatic dependency resolution ([architecture RFC 0019](https://github.com/Kuadrant/architecture/pull/179)). mcp-gateway is currently packaged as a standalone OLM operator with a declared dependency on `kuadrant-operator >=1.4.3`. This dependency declaration will be rejected under OLMv1.

Rather than creating a separate umbrella operator, the team has decided that the kuadrant-operator itself should evolve to manage component deployment via embedded Helm charts. mcp-gateway is the first component to prove this pattern. If successful, other components (Limitador, Authorino, dns-operator) can follow the same model.

The [umbrella-operator mcp-gateway-poc](https://github.com/Kuadrant/umbrella-operator/tree/mcp-gateway-poc) branch demonstrates the Helm rendering, server-side apply, and migration mechanics that will be ported into the kuadrant-operator.

mcp-gateway's multi-tenant deployment model (per-namespace broker-router instances) differs from Kuadrant's singleton model (one Authorino, one Limitador per cluster). These models are compatible — see Multi-Tenancy Analysis below.

## Summary

The kuadrant-operator manages mcp-gateway as an optional component. When enabled via the Kuadrant CR, the kuadrant-operator deploys the mcp-gateway controller as a cluster singleton in headless mode and watches MCPGatewayExtension CRs. When a user creates an MCPGatewayExtension CR, the kuadrant-operator renders the mcp-gateway Helm chart per-namespace to create broker-router infrastructure. The mcp-gateway controller continues to handle MCPServerRegistration and MCPVirtualServer reconciliation only. mcp-gateway requires a headless mode flag, network policy templates, chart publishing, and OLM dependency removal.

## Goals

- Add controller headless mode (`DISABLE_EXTENSION_RECONCILER`) so the kuadrant-operator can manage broker-router infrastructure
- Add default network policies for broker-router and controller pods (CONNLINK-1115)
- Remove OLMv1-incompatible dependency declarations
- Publish the mcp-gateway Helm chart to `kuadrant.io/helm-charts/`
- Prove the Helm-based component deployment pattern with mcp-gateway as the first component
- Define migration path for existing standalone OLM installations

## Non-Goals

- Changes to Kuadrant's Authorino or Limitador deployment model (those follow later if the pattern works)
- Multi-cluster federation
- Full Kuadrant multi-tenancy (per-tenant Authorino/Limitador instances)
- Deprecating the standalone OLM bundle — both install paths (standalone and kuadrant-operator-managed) will be maintained
- Splitting the kuadrant-operator's policy controller into a separate component (possible future work, not required for this phase)

## Job Stories

### When I install Kuadrant with MCP Gateway support

When a platform engineer installs Kuadrant, they want to enable mcp-gateway as an optional component so that MCP server aggregation and routing is available alongside auth and rate limiting without a separate installation step.

### When I upgrade Kuadrant and mcp-gateway together

When a platform engineer upgrades Kuadrant via OLM or Helm, they want the kuadrant-operator to upgrade the mcp-gateway controller with version-pinned images so that all components stay compatible and the upgrade is coordinated.

### When I migrate from standalone mcp-gateway to kuadrant-managed

When a platform engineer has mcp-gateway installed via its own OLM subscription, they want a documented migration path to the kuadrant-operator-managed model so that existing MCPGatewayExtensions, broker-router deployments, and MCP server registrations continue working without disruption.

### When multiple teams share a Kuadrant-managed gateway

When multiple teams each have their own MCPGatewayExtension in their namespace, they want their MCP server catalogs, auth policies, and rate limits to be isolated from other teams so that one team's configuration cannot affect another team's traffic, even though Authorino and Limitador are shared singletons.

### When I want Kuadrant without mcp-gateway

When a platform engineer installs Kuadrant but does not need MCP Gateway functionality, they want to omit mcp-gateway entirely so that no unnecessary CRDs, RBAC, or controller deployments are created.

## Design

### Two Deployment Models

mcp-gateway supports two deployment models. The controller binary is the same in both — the mode is controlled by an environment variable.

**Standalone mode** (current default): the mcp-gateway controller runs all three reconcilers — MCPGatewayExtensionReconciler (creates broker-router infrastructure), MCPReconciler (writes MCP server config), and MCPVirtualServerReconciler (writes virtual server config). The controller owns the full lifecycle of broker-router Deployments, Services, EnvoyFilters, HTTPRoutes, and Secrets.

**Headless mode** (`DISABLE_EXTENSION_RECONCILER=true`): the controller only runs MCPReconciler and MCPVirtualServerReconciler. It does not register the MCPGatewayExtensionReconciler, does not watch for Deployments/Services/EnvoyFilters, and does not create or manage broker-router infrastructure. An external system (the kuadrant-operator) is responsible for creating that infrastructure.

### Kuadrant-Operator Integration Architecture

The kuadrant-operator manages mcp-gateway using the same Helm rendering and server-side apply pattern demonstrated in the [umbrella-operator mcp-gateway-poc](https://github.com/Kuadrant/umbrella-operator/tree/mcp-gateway-poc) branch:

```
kuadrant-operator (kuadrant-system):
  ├── kuadrant-operator Deployment (policy controller + component manager)
  ├── Authorino Deployment (cluster-wide)
  ├── Limitador Deployment (cluster-wide)
  ├── dns-operator Deployment
  └── mcp-gateway-controller Deployment (headless mode, singleton)

User creates MCPGatewayExtension CR in team-a namespace
  → kuadrant-operator renders mcp-gateway chart with controller.enabled=false
  → Applies to team-a namespace:
      ├── broker-router Deployment
      ├── Service
      ├── ServiceAccount
      ├── HTTPRoute(s)
      ├── Config Secret (mcp-gateway-config)
      ├── NetworkPolicy (when enabled)
      └── EnvoyFilter (in Gateway namespace)

mcp-gateway-controller (headless, kuadrant-system):
  → Watches MCPServerRegistration CRs across all namespaces
  → Writes config to mcp-gateway-config Secret in each extension namespace
  → Watches MCPVirtualServer CRs
  → Does NOT create broker-routers, EnvoyFilters, or HTTPRoutes
```

The Helm rendering mechanics (`renderChart`, `buildValues`, `splitManifests`, `applyManifest`) and migration logic (`migration.go`) from the [umbrella-operator POC](https://github.com/Kuadrant/umbrella-operator/tree/mcp-gateway-poc/internal/controller) serve as the reference implementation. The key functions:

- `renderChart()` — loads the mcp-gateway chart, runs Helm template in client-only dry-run mode scoped to the CR's namespace
- `buildValues()` — translates MCPGatewayExtension CR spec into chart values
- `applyManifest()` — applies each rendered resource via server-side apply with field ownership
- `needsMigration()` / `migrate()` — detects standalone controller-owned resources and takes over

#### Opt-in Mechanism

mcp-gateway is optional within Kuadrant. Users enable it via the Kuadrant CR:

```yaml
apiVersion: kuadrant.io/v1beta1
kind: Kuadrant
spec:
  components:
    mcpGateway:
      enabled: true
```

This follows the existing pattern for optional components — the Kuadrant CR already has `spec.components.developerPortal.enabled` (see `api/v1beta1/kuadrant_types.go`). When disabled, the kuadrant-operator does not deploy the mcp-gateway controller or watch MCPGatewayExtension CRs.

#### Per-Namespace Chart Rendering

The kuadrant-operator renders the mcp-gateway Helm chart once per MCPGatewayExtension CR, scoped to the CR's namespace. It calls `buildValues()` to translate the CR spec into chart values and applies manifests via server-side apply with field ownership.

The chart must be renderable with `controller.enabled: false` (skip the controller Deployment — the kuadrant-operator deploys it separately as a singleton) and with values derived from the MCPGatewayExtension spec (targetRef, publicHost, sessionStore, trustedHeadersKey, etc.).

### Controller Headless Mode

#### Environment Variable

`DISABLE_EXTENSION_RECONCILER=true` disables the MCPGatewayExtensionReconciler. The controller reads this env var at startup (`cmd/main.go`) and conditionally skips registering the reconciler.

When disabled:
- MCPGatewayExtensionReconciler is not registered — no watches on Deployments, Services, Gateways, EnvoyFilters, Secrets
- No broker-router Deployments, Services, ServiceAccounts, HTTPRoutes, or EnvoyFilters are created
- `RELATED_IMAGE_ROUTER_BROKER` and `BROKER_ROUTER_LOG_LEVEL` env vars are not needed

When enabled (default, standalone mode):
- All three reconcilers run as today — no behavior change

#### Index Extraction

The MCPReconciler depends on field indexes (`gatewayIndexKey`, `refGrantIndexKey`) currently set up inside `MCPGatewayExtensionReconciler.SetupWithManager()`. These indexes are used by `MCPGatewayExtensionValidator.FindValidMCPGatewayExtsForGateway()` to find valid extensions for a gateway.

In headless mode, the MCPGatewayExtensionReconciler is not registered, but the MCPReconciler still needs these indexes. The index setup must be extracted into an exported function (`SetupRequiredIndexes`) called unconditionally from `cmd/main.go` before any reconciler registration.

#### Helm Chart Values

```yaml
controller:
  enabled: true
  disableExtensionReconciler: false
```

The Helm chart's `deployment-controller.yaml` template sets the `DISABLE_EXTENSION_RECONCILER` env var from this value.

### Network Policies

Default NetworkPolicy templates for broker-router and controller pods ([CONNLINK-1115](https://redhat.atlassian.net/browse/CONNLINK-1115)). NetworkPolicy is namespace-scoped, so each namespace with a broker-router gets its own policy — the chart template is written once and stamped per-namespace by the rendering.

#### Broker-Router NetworkPolicy

```yaml
# Ingress
- port 8080/TCP (HTTP — MCP protocol, health checks)
- port 50051/TCP (gRPC — ext_proc from Envoy)

# Egress
- All (broker-router must reach arbitrary upstream MCP servers and the Gateway for hairpin requests)
```

#### Controller NetworkPolicy

```yaml
# Ingress
- port 8082/TCP (Prometheus metrics scraping)

# Egress
- port 443/TCP (kube-apiserver)
- DNS (port 53/TCP+UDP)
```

Both policies are gated by `networkPolicy.enabled` in chart values, defaulting to `false`.

### Chart Publishing

The mcp-gateway Helm chart must be published to `https://kuadrant.io/helm-charts/` following the same pattern as authorino-operator, limitador-operator, and dns-operator:

1. Package the chart as a `.tgz` with semantic versioning
2. Publish to the [Kuadrant/helm-charts](https://github.com/Kuadrant/helm-charts) repository
3. Update the `index.yaml` in that repository

The existing `.github/workflows/helm-release.yaml` already handles chart packaging and GHCR OCI publishing. It must be extended to also publish to `kuadrant.io/helm-charts/`.

### Phased Rollout

mcp-gateway is the first component to prove the Helm-based deployment pattern within the kuadrant-operator. The phased approach:

1. **Phase 1 (this design)**: mcp-gateway — headless mode, chart publishing, kuadrant-operator POC. Proves the pattern of rendering an embedded Helm chart per-namespace, server-side apply, and migration from standalone.
2. **Phase 2**: Limitador — apply the same pattern to Limitador deployment. The kuadrant-operator already manages Limitador but could standardize on Helm rendering.
3. **Phase 3**: Authorino, dns-operator — extend to remaining components as needed.

Each phase is independent. The first phase with mcp-gateway alone is sufficient to validate the approach and ship a working integration.

### CRD Lifecycle

CRD management is an open design question with multiple approaches under consideration:

- **Init containers** (like [cluster-olm-operator](https://github.com/openshift/cluster-olm-operator/blob/main/manifests/0000_51_olm_06_deployment.yaml#L28-L41)): the kuadrant-operator pod runs init containers that apply CRDs before the main container starts. This decouples CRD installation from OLM but requires the operator to bundle all component CRDs.
- **Bundled in Helm chart**: CRDs live in the chart's `crds/` directory. Helm installs them on first install but does not upgrade or delete them. CRD upgrades require `kubectl apply`. The kuadrant-operator could run this step explicitly.
- **OLM-managed** (current approach): CRDs are in the OLM bundle. OLM handles install, upgrade, and safety checks. Under OLMv1 single-ownership, only one bundle can own a CRD.

The approach chosen affects whether the kuadrant-operator and standalone mcp-gateway can coexist on the same cluster. This decision is not resolved in this design doc — it applies to all components (Authorino, Limitador, dns-operator) and should be decided at the kuadrant-operator level.

For the mcp-gateway POC, the simplest path is to bundle CRDs in the chart and have the kuadrant-operator apply them when `spec.components.mcpGateway.enabled` is set.

### Upgrade Ordering

The kuadrant-operator coordinates upgrades. mcp-gateway-controller fits alongside other dependency controllers:

1. CRDs and RBAC (including mcp-gateway CRDs)
2. Workloads (Authorino, Limitador) + dependency controllers (dns-operator, **mcp-gateway-controller**)
3. kuadrant-operator policy reconciler (may depend on new capabilities from updated workloads)

mcp-gateway-controller has no dependency on Authorino or Limitador. It depends on Gateway API and Istio CRDs being present, which are cluster prerequisites managed outside the kuadrant-operator.

**Rollback**: mcp-gateway-controller can be rolled back independently of other components. The controller only manages its own CRDs (MCPGatewayExtension, MCPServerRegistration, MCPVirtualServer) and does not interact with Authorino, Limitador, or the policy controller at runtime. Rolling back the controller image reverts the Deployment; existing broker-router instances continue running with the previous config until the rolled-back controller reconciles them. CRD rollback (removing fields) is not supported — use forward-fix.

### Multi-Tenancy Analysis

mcp-gateway deploys isolated broker-router instances per namespace. Kuadrant uses shared singleton Authorino and Limitador instances. Tenant isolation is maintained: Authorino uses SHA256-hashed route identifiers (not real hostnames) for AuthConfig lookup, so each tenant's auth configuration is keyed to their unique namespace-qualified route path. Limitador partitions rate limit counters by route-derived namespaces and policy-specific identifiers, so one tenant's traffic cannot increment another's counters. Cross-tenant interference is structurally impossible at both layers. Verified against kuadrant-operator v1.4.2, authorino v1.1.2, limitador v0.12.0, wasm-shim v0.9.0.

### Migration from Standalone OLM Install

Migration is handled by the kuadrant-operator using the same pattern demonstrated in the [umbrella-operator POC's migration.go](https://github.com/Kuadrant/umbrella-operator/blob/mcp-gateway-poc/internal/controller/migration.go):

1. **Detect**: the kuadrant-operator checks if broker-router Deployments have ownerReferences pointing to the MCPGatewayExtension CR (indicating a standalone mcp-gateway controller manages them).
2. **Scale down**: the kuadrant-operator scales the standalone mcp-gateway controller to 0 replicas. Existing broker-routers continue running.
3. **Strip ownerReferences**: the kuadrant-operator removes ownerReferences from broker-router Deployments, Services, ServiceAccounts, HTTPRoutes, and Secrets so they are not cascade-deleted.
4. **Apply**: the kuadrant-operator re-applies the resources via server-side apply with its own field ownership. The headless mcp-gateway controller (deployed by the kuadrant-operator) takes over MCPServerRegistration/MCPVirtualServer reconciliation.

**Control plane gap**: between controller scale-down and kuadrant-operator apply, no controller is watching MCPGatewayExtension CRs. Existing broker-routers continue serving traffic, but new MCPGatewayExtension or MCPServerRegistration changes are not reconciled until the kuadrant-operator's controller is running.

### Standalone OLM Bundle

The standalone OLM bundle (`bundle/`) continues to be maintained. Users who install mcp-gateway independently (without the kuadrant-operator) still use the OLM subscription path. The only change is removing the `olm.package.required` dependency declaration so the bundle is OLMv1-compatible. The CSV, owned CRDs, and RBAC remain unchanged.

Both install paths consume the same controller image. In standalone mode, the controller runs with `DISABLE_EXTENSION_RECONCILER` unset (default, full mode). In kuadrant-operator-managed mode, the controller runs with `DISABLE_EXTENSION_RECONCILER=true` (headless mode).

### Prerequisites

- Gateway API CRDs must be present on the cluster
- Istio must be installed (mcp-gateway creates EnvoyFilters — Istio is a hard requirement, not configurable)

## Security Considerations

- **No new attack surface**: the mcp-gateway controller already has cluster-wide RBAC for watching CRDs. Moving management into the kuadrant-operator does not expand mcp-gateway's permissions.
- **Headless mode reduces surface**: in headless mode, the controller does not watch or create Deployments, Services, or EnvoyFilters, reducing the scope of resources it can modify.
- **Network policies**: default NetworkPolicy templates restrict ingress to broker-router pods to expected traffic (Envoy ext_proc on 50051, HTTP on 8080) and controller pods to metrics scraping (8082).
- **CRD ownership**: if standalone mcp-gateway and kuadrant-operator are both present, CRD ownership conflicts under OLMv1's single-ownership model. The migration path above addresses this.
- **Image provenance**: the kuadrant-operator pins image references at build time. The chart's `RELATED_IMAGE_ROUTER_BROKER` env var propagates the pinned broker-router image.

## Relationship to Existing Approaches

- **RFC 0019** (architecture#179): this design implements mcp-gateway integration within the kuadrant-operator itself, rather than a separate umbrella operator. The Helm rendering pattern is the same.
- **Umbrella-operator POC** ([Kuadrant/umbrella-operator mcp-gateway-poc](https://github.com/Kuadrant/umbrella-operator/tree/mcp-gateway-poc)): the reference implementation for Helm rendering (`helm.go`), value mapping (`buildValues`), server-side apply (`applyManifest`), and migration logic (`migration.go`). These patterns will be ported into the kuadrant-operator.
- **Operator-based install** (`docs/design/operator-based-install.md`): the operator deployment model supports both standalone and headless modes. In standalone mode, the controller works as designed. In headless mode, infrastructure creation is delegated to the kuadrant-operator.
- **Isolated gateway deployment** (`docs/design/isolated-gateway-deployment.md`): the per-namespace isolation model is preserved in both modes. The kuadrant-operator renders per-namespace, maintaining the same isolation boundaries.

## Future Considerations

- **Kuadrant extensions SDK**: issues [#1609](https://github.com/Kuadrant/kuadrant-operator/issues/1609) and [#1612](https://github.com/Kuadrant/kuadrant-operator/issues/1612) define a registration mechanism via `pkg/extension/` with gRPC proto definitions. The Helm-based deployment and the extensions SDK are complementary — Helm handles deploying the mcp-gateway controller and broker-routers, while the extensions SDK could handle runtime integration (topology participation, policy resolution). If the SDK defines a registration CR, the kuadrant-operator integration should be updated to create it.
- **Selective component deployment**: the `spec.components` field on the Kuadrant CR gates each component. mcp-gateway follows the `DeveloperPortal` pattern.
- **Per-tenant Authorino/Limitador**: if Kuadrant later supports per-Gateway or per-namespace operand instances, mcp-gateway benefits automatically — each team's HTTPRoutes would get dedicated auth and rate limiting backends.
- **Policy controller separation**: the kuadrant-operator currently contains both policy reconciliation and component deployment. These could be split into separate binaries in the future without changing the mcp-gateway integration — the headless mode and chart rendering are independent of the policy machinery.

## Execution

See [tasks/tasks.md](tasks/tasks.md) for the implementation plan.

No new e2e test cases for the design doc itself. Task 1 (headless mode) and Task 2 (network policies) include their own test requirements.

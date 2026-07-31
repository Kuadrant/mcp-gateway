# Kuadrant Operator Integration

## Problem

OLMv1 removes automatic dependency resolution ([architecture RFC 0019](https://github.com/Kuadrant/architecture/pull/179)). mcp-gateway is currently packaged as a standalone OLM operator with a declared dependency on `kuadrant-operator >=1.4.3`. This dependency declaration is not supported under OLMv1.

Rather than creating a separate umbrella operator, the kuadrant-operator itself evolves to manage component deployment via embedded Helm charts. mcp-gateway is the first component to prove this pattern alongside authorino-operator, limitador-operator, and dns-operator.

## Summary

The kuadrant-operator manages mcp-gateway as a child operator. When a user creates a Kuadrant CR, the kuadrant-operator deploys the mcp-gateway controller alongside other child operators (Authorino, Limitador, DNS Operator) using Helm chart rendering and server-side apply. The mcp-gateway controller then manages broker-router instances per MCPGatewayExtension CR as normal. mcp-gateway requires network policy templates, chart structure alignment, and OLM dependency removal.

## Goals

- Add default NetworkPolicy templates for broker-router and controller pods (CONNLINK-1115)
- Remove OLMv1-incompatible dependency declarations
- Publish the mcp-gateway Helm chart to `kuadrant.io/helm-charts/`
- Align chart structure with the child operator convention (ClusterRoles in `static/`, runtime resources in `templates/`)
- Prove the Helm-based component deployment pattern with mcp-gateway

## Non-Goals

- Changes to Kuadrant's Authorino or Limitador deployment model
- Multi-cluster federation
- Full Kuadrant multi-tenancy (per-tenant Authorino/Limitador instances)
- Deprecating the standalone OLM bundle immediately — migration path is defined separately

## Job Stories

### When I install Kuadrant with MCP Gateway support

When a platform engineer installs Kuadrant, they want to enable mcp-gateway as an optional component so that MCP server aggregation and routing is available alongside auth and rate limiting without a separate installation step.

### When I upgrade Kuadrant and mcp-gateway together

When a platform engineer upgrades Kuadrant via OLM or Helm, they want the kuadrant-operator to upgrade the mcp-gateway controller with version-pinned images so that all components stay compatible and the upgrade is coordinated.

### When I migrate from standalone mcp-gateway to kuadrant-managed

When a platform engineer has mcp-gateway installed via its own OLM subscription, they want a documented migration path to the kuadrant-operator-managed model so that existing MCPGatewayExtensions, broker-router deployments, and MCP server registrations continue working without disruption.

### When multiple teams share a Kuadrant-managed gateway

When multiple teams each have their own MCPGatewayExtension in their namespace, they want their MCP server catalogs, auth policies, and rate limits to be isolated from other teams so that one team's configuration cannot affect another team's traffic.

### When I want Kuadrant without mcp-gateway

When a platform engineer installs Kuadrant but does not need MCP Gateway functionality, they want to omit mcp-gateway entirely so that no unnecessary CRDs, RBAC, or controller deployments are created.

## Design

### Architecture

The kuadrant-operator manages mcp-gateway using the same Helm rendering and server-side apply pattern as other child operators. When the user creates a Kuadrant CR:

```
kuadrant-operator (triggered by Kuadrant CR):
  ├── Authorino Deployment
  ├── Limitador Deployment
  ├── dns-operator Deployment
  └── mcp-gateway-controller Deployment  ← same pattern

mcp-gateway-controller (running normally):
  → Watches MCPGatewayExtension CRs
  → Per MCPGatewayExtension: creates broker-router Deployment, Service,
    ServiceAccount, HTTPRoute(s), Config Secret, EnvoyFilter
  → Watches MCPServerRegistration and MCPVirtualServer CRs
```

The mcp-gateway controller runs in full mode — it manages broker-router instances per MCPGatewayExtension exactly as in the standalone model. The only difference is that the controller itself is deployed by kuadrant-operator rather than by a separate OLM subscription.

### Cluster-Scoped vs Namespaced Resource Separation

Following the pattern established across all child operator charts:

**Cluster-scoped resources** (CRDs, ClusterRoles) are pre-installed by Helm or OLM before the operator starts. They live in `charts/mcp-gateway/crds/` and `charts/mcp-gateway/static/clusterroles.yaml`. The kuadrant-operator never modifies these at runtime.

**Namespaced resources** (Deployments, ServiceAccounts, Roles, RoleBindings, ConfigMaps, Services) and ClusterRoleBindings are created by the kuadrant-operator at runtime when the Kuadrant CR is created. These live in `charts/mcp-gateway/templates/`.

The `make sync-child-operator-charts` target in kuadrant-operator extracts CRDs and ClusterRoles from the chart into `config/dependencies/child-operators/` and folds them into the kuadrant-operator's own OLM bundle and Helm chart. kuadrant-operator holds `bind/escalate` on `mcp-gateway-controller` ClusterRole, enabling it to create ClusterRoleBindings for the mcp-gateway controller ServiceAccount without holding all its permissions.

### Chart Structure

```
charts/mcp-gateway/
├── crds/                    # Pre-installed by Helm/OLM (cluster-scoped)
│   └── mcp.kuadrant.io_*.yaml
├── static/                  # Pre-installed by Helm/OLM (cluster-scoped)
│   └── clusterroles.yaml    # mcp-gateway-controller ClusterRole (hardcoded name)
└── templates/               # Applied at runtime by kuadrant-operator
    ├── deployment-controller.yaml
    ├── serviceaccount.yaml
    ├── rbac.yaml            # ClusterRoleBinding only
    ├── networkpolicy-broker-router.yaml
    └── networkpolicy-controller.yaml
```

The ClusterRole name is hardcoded as `mcp-gateway-controller` rather than derived from the Helm release name. ClusterRoles are cluster-scoped — the `fullname` pattern is appropriate for namespaced resources where multiple instances coexist, but not for ClusterRoles where a stable name is required for `bind/escalate` resourceNames.

### Component deployment

> **Updated per RFC 0019 (merged 2026-07-29):** Conditional component deployment is
> deferred to a future RFC. All component controllers (including mcp-gateway) deploy
> unconditionally on kuadrant-operator startup. The `spec.components.mcpGateway.enabled`
> field described in the original design is not part of the initial implementation.

mcp-gateway controller runs as a cluster singleton in the kuadrant-operator namespace
alongside authorino-operator, limitador-operator, and dns-operator. No Kuadrant CR or
additional configuration is needed to start it.

### Network Policies

NetworkPolicy templates for broker-router and controller pods ([CONNLINK-1115](https://redhat.atlassian.net/browse/CONNLINK-1115)). Disabled by default (`networkPolicy.enabled: false`) because clusters without a NetworkPolicy enforcement engine silently ignore them, and OCP clusters may have their own network policy model. Users enable them explicitly when their cluster enforces NetworkPolicy.

#### Broker-Router NetworkPolicy

```yaml
# Ingress
- port 8080/TCP (HTTP — MCP protocol, health checks)
- port 50051/TCP (gRPC — ext_proc from Envoy)

# Egress
- All (broker-router must reach arbitrary upstream MCP servers and
  the Gateway for hairpin requests)
```

#### Controller NetworkPolicy

```yaml
# Ingress
- port 8081/TCP (health probes — liveness/readiness)
- port 8082/TCP (Prometheus metrics scraping)

# Egress
- port 443/TCP (kube-apiserver)
- port 6443/TCP (kube-apiserver — non-standard configurations)
- DNS (port 53/TCP+UDP)
```

### Chart Publishing

The mcp-gateway Helm chart must be published to `https://kuadrant.io/helm-charts/` following the same pattern as authorino-operator, limitador-operator, and dns-operator:

1. Package the chart as a `.tgz` with semantic versioning
2. Publish to the [Kuadrant/helm-charts](https://github.com/Kuadrant/helm-charts) repository
3. Update the `index.yaml` in that repository

The kuadrant-operator syncs the chart via `make sync-child-operator-charts MCP_GATEWAY_VERSION=<tag>` pinned to a specific release.

### CRD Lifecycle

CRDs are extracted from `charts/mcp-gateway/crds/` during `make sync-child-operator-charts` and included in the kuadrant-operator OLM bundle and Helm chart. Under OLMv1 single-ownership, kuadrant-operator owns the mcp CRDs — the standalone mcp-gateway OLM bundle no longer declares them.

### Upgrade Ordering

The kuadrant-operator coordinates upgrades:

1. CRDs and RBAC (including mcp-gateway CRDs and ClusterRole)
2. Child controller Deployments (Authorino, Limitador, dns-operator, mcp-gateway-controller)
3. kuadrant-operator policy reconciler

mcp-gateway-controller has no dependency on Authorino or Limitador. It depends on Gateway API and Istio CRDs being present as cluster prerequisites.

### Multi-Tenancy Analysis

mcp-gateway deploys isolated broker-router instances per namespace. Kuadrant uses shared singleton Authorino and Limitador instances. Tenant isolation is maintained: Authorino uses SHA256-hashed route identifiers for AuthConfig lookup so each tenant's auth configuration is keyed to their unique namespace-qualified route path. Limitador partitions rate limit counters by route-derived namespaces and policy-specific identifiers. Cross-tenant interference is structurally impossible at both layers.

### Migration from Standalone OLM Install

See `olm-upgrade-guide.md` for the step-by-step procedure. The key property that enables zero-downtime migration: broker-router resources are owned by the MCPGatewayExtension CR via ownerRef, not by the operator CSV. Deleting the standalone mcp-gateway subscription and CSV does not affect running broker-routers.

### Standalone OLM Bundle

The standalone OLM bundle (`bundle/`) will eventually be retired once kuadrant-operator takes over OLM packaging. In the interim the `olm.maxOpenShiftVersion` annotation has been removed so the bundle is installable on OCP 4.18+. Tracked in issue #1107.

## Security Considerations

- **No new attack surface**: the mcp-gateway controller already has cluster-wide RBAC for watching CRDs. Moving management into kuadrant-operator does not expand mcp-gateway's permissions.
- **Network policies**: default NetworkPolicy templates restrict ingress to broker-router pods to expected traffic (Envoy ext_proc on 50051, HTTP on 8080) and controller pods to metrics scraping (8082). Disabled by default, opt-in per cluster.
- **CRD ownership**: under OLMv1 single-ownership, only kuadrant-operator owns mcp CRDs. The standalone mcp-gateway OLM bundle no longer declares them, avoiding conflicts.
- **Image provenance**: kuadrant-operator pins mcp-gateway image references at build time via `MCP_GATEWAY_GITREF` in `make sync-child-operator-charts`.

## Relationship to Existing Approaches

- **RFC 0019** (architecture#179): this design implements mcp-gateway integration within kuadrant-operator itself, following the umbrella operator pattern.
- **olmv1-umbrella-poc-phase1** ([Kuadrant/kuadrant-operator](https://github.com/Kuadrant/kuadrant-operator/tree/olmv1-umbrella-poc-phase1)): Mike Nairn's POC branch — the reference implementation for Helm rendering, server-side apply, and the cluster-scoped vs namespaced resource separation.
- **Isolated gateway deployment** (`docs/design/isolated-gateway-deployment.md`): the per-namespace isolation model is preserved. The controller continues to manage per-namespace broker-router instances via MCPGatewayExtension.

## Future Considerations

- **Kuadrant extensions SDK**: issues [#1609](https://github.com/Kuadrant/kuadrant-operator/issues/1609) and [#1612](https://github.com/Kuadrant/kuadrant-operator/issues/1612) define a registration mechanism. Helm-based deployment and the extensions SDK are complementary.
- **Helm chart internals**: Mike Nairn has noted doubts about whether the Helm rendering approach is the right long-term choice for internal use. An alternative is embedding static manifests as Go assets and applying them directly. This does not affect the mcp-gateway chart structure — only the kuadrant-operator side.

## Execution

Implementation is tracked in [epic #1226](https://github.com/Kuadrant/mcp-gateway/issues/1226) with sub-issues for each task.

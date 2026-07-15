# Controller Responsibility Split: Standalone vs. Umbrella Operator

Reference document for the responsibility boundary between the mcp-gateway controller and
kuadrant-operator. Covers who owns what in each installation mode and the rationale for the
current split.

---

## Side-by-side comparison

| Concern | Standalone (Helm) | Umbrella operator (OLM) |
|---|---|---|
| **CRDs** | Helm `crds/` dir (`charts/mcp-gateway/crds/`) | OLM bundle (CSV owns MCPGatewayExtension, MCPServerRegistration, MCPVirtualServer) |
| **Controller Deployment** | Helm chart templates (`charts/mcp-gateway`) | kuadrant-operator renders `charts/mcp-gateway` via `pkg/helm.Renderer` + dynamic client SSA |
| **Controller ServiceAccount / RBAC** | Helm chart templates | kuadrant-operator renders `charts/mcp-gateway` via `pkg/helm.Renderer` + dynamic client SSA |
| **Broker-router Deployment** | mcp-gateway controller | mcp-gateway controller (unchanged) |
| **Broker-router Service** | mcp-gateway controller | mcp-gateway controller (unchanged) |
| **Broker-router ServiceAccount** | mcp-gateway controller | mcp-gateway controller (unchanged) |
| **Broker-router HTTPRoutes** | mcp-gateway controller | mcp-gateway controller (unchanged) |
| **Broker-router cleanup on extension delete** | mcp-gateway controller (ownerRef cascade) | mcp-gateway controller (ownerRef cascade, unchanged) |
| **mcp-gateway-config Secret** | mcp-gateway controller | mcp-gateway controller (unchanged) |
| **Session signing key Secret** | mcp-gateway controller | mcp-gateway controller (unchanged) |
| **Trusted headers keypair Secrets** | mcp-gateway controller | mcp-gateway controller (unchanged) |
| **EnvoyFilter** | mcp-gateway controller | mcp-gateway controller (unchanged) |
| **Gateway listener status** | mcp-gateway controller | mcp-gateway controller (unchanged) |
| **MCPServerRegistration reconciliation** | mcp-gateway controller | mcp-gateway controller (unchanged) |
| **MCPVirtualServer reconciliation** | mcp-gateway controller | mcp-gateway controller (unchanged) |

The controller runs identically in both modes. The only difference is who deploys the
controller itself.

---

## Installation flow diagrams

### Standalone (Helm)

```
User runs: helm install mcp-gateway charts/mcp-gateway
│
├── Helm applies crds/ directory
│     MCPGatewayExtension CRD
│     MCPServerRegistration CRD
│     MCPVirtualServer CRD
│
├── Helm applies templates/
│     ServiceAccount      (controller)
│     ClusterRole         (controller RBAC)
│     ClusterRoleBinding  (controller RBAC)
│     Deployment          (mcp-controller)
│     MCPGatewayExtension (pre-configured CR — user edits values.yaml)
│     Gateway             (optional, gated by gateway.create)
│
└── mcp-controller starts
      │
      ├── On MCPGatewayExtension create/update:
      │     validates namespace + listener conflicts
      │     creates mcp-gateway-config Secret
      │     creates session-signing-key Secret
      │     creates trusted-headers keypair Secrets     (if spec.trustedHeadersKey.generate=Enabled)
      │     validates session store Secret              (if spec.sessionStore set)
      │     creates ServiceAccount                      (broker-router)
      │     creates Deployment                          (broker-router)
      │     creates Service                             (broker-router)
      │     creates HTTPRoute /mcp /status /.well-known
      │     creates HTTPRoute /tokens                   (if spec.urlElicitation=Enabled)
      │     creates EnvoyFilter                         (in gateway namespace)
      │     updates Gateway listener status
      │
      ├── On MCPServerRegistration create/update:
      │     resolves upstream URL from HTTPRoute backends
      │     reads credentialRef Secret
      │     reads caCertSecretRef Secret
      │     writes server entry to mcp-gateway-config Secret
      │     updates HTTPRoute status
      │
      └── On MCPVirtualServer create/update:
            writes virtual server tools/prompts to mcp-gateway-config Secret
```

### Umbrella operator (OLM / kuadrant-operator)

```
OLM installs kuadrant-operator bundle
│
├── OLM applies CRDs from bundle manifests
│     Kuadrant, AuthPolicy, RateLimitPolicy, DNSPolicy, TLSPolicy, ...
│     MCPGatewayExtension CRD      ← bundled from mcp-gateway
│     MCPServerRegistration CRD    ← bundled from mcp-gateway
│     MCPVirtualServer CRD         ← bundled from mcp-gateway
│
└── OLM starts kuadrant-operator Deployment
      │
      ├── kuadrant-operator starts
      │     detects MCPGatewayExtension CRD is installed (IsCRDInstalled check)
      │     registers MCPGatewayExtension watcher (all namespaces, unstructured)
      │     wires MCPGatewayReconciler into SOTW workflow
      │
      └── On any MCPGatewayExtension event (and at startup):
            MCPGatewayReconciler.reconcileController()
              pkg/helm.Renderer renders charts/mcp-gateway (SkipCRDs=true)
              dynamic client SSA applies per resource (GVR-based Apply):
                ServiceAccount      (mcp-controller, in operator namespace)
                ClusterRole         (mcp-controller RBAC)
                ClusterRoleBinding  (mcp-controller RBAC)
                Deployment          (mcp-controller)

      mcp-controller starts
      │
      ├── On MCPGatewayExtension create/update:
      │     (identical to standalone — controller runs in full mode)
      │     validates namespace + listener conflicts
      │     creates mcp-gateway-config Secret
      │     creates session-signing-key Secret
      │     creates trusted-headers keypair Secrets     (if spec.trustedHeadersKey.generate=Enabled)
      │     validates session store Secret              (if spec.sessionStore set)
      │     creates ServiceAccount                      (broker-router)
      │     creates Deployment                          (broker-router)
      │     creates Service                             (broker-router)
      │     creates HTTPRoute /mcp /status /.well-known
      │     creates HTTPRoute /tokens                   (if spec.urlElicitation=Enabled)
      │     creates EnvoyFilter                         (in gateway namespace)
      │     updates Gateway listener status
      │
      ├── On MCPServerRegistration create/update:
      │     (identical to standalone)
      │
      └── On MCPVirtualServer create/update:
            (identical to standalone)
```

---

## What the mcp-gateway controller does in each mode

The controller runs identically in both modes. All functions run in both standalone and
umbrella operator installations.

### MCPGatewayExtensionReconciler

| Function | Resource | Standalone | Umbrella | Team decision |
|---|---|---|---|---|
| Namespace conflict check | — | runs | runs | |
| Gateway target validation + ReferenceGrant | — | runs | runs | |
| Listener conflict check | — | runs | runs | |
| EnsureConfigExists | `mcp-gateway-config` Secret | runs | runs | |
| reconcileTrustedHeaders | 0–2 `Secret` objects (keypair) | runs | runs | |
| reconcileSessionSigningKey | `mcp-gateway-session-signing-key` Secret | runs | runs | |
| validateSessionStore | — | runs | runs | |
| reconcileServiceAccount | `ServiceAccount` (broker-router) | runs | runs | |
| reconcileDeployment | `Deployment` (broker-router) | runs | runs | |
| reconcileService | `Service` (broker-router) | runs | runs | |
| reconcileGatewayHTTPRoute | `HTTPRoute` | runs | runs | |
| reconcileTokensHTTPRoute | `HTTPRoute` (tokens) | runs | runs | |
| reconcileEnvoyFilter | `EnvoyFilter` in gateway namespace | runs | runs | |
| updateGatewayListenerStatus | `Gateway` status | runs | runs | |
| updateStatus | `MCPGatewayExtension` status | runs | runs | |
| handleDeletion | removes EnvoyFilter, clears config | runs | runs | |

### MCPReconciler (MCPServerRegistration)

All functions run identically in both modes.

### MCPVirtualServerReconciler

All functions run identically in both modes.

---

## Why the current split is where it is

The only thing kuadrant-operator does is deploy the mcp-gateway controller binary itself.
Everything the controller does — broker-router lifecycle, secrets, EnvoyFilter, config,
validation, Gateway status — stays in the controller in both modes.

**Why not have kuadrant-operator create broker-router resources directly?**

- The controller already has the logic, watches, and ownerRef-based GC for broker-router
  resources. Duplicating this in kuadrant-operator means maintaining the same logic twice
  and losing ownerRef cascade cleanup (cross-namespace ownerRefs are not allowed for
  namespace-scoped resources, so kuadrant-operator would need label-based GC instead).
- The broker-router Deployment flags are synthesised from live cluster state (listener port,
  Gateway hostname, session store secret reference). The controller already derives these;
  replicating that derivation in kuadrant-operator adds no value.
- The standalone Helm chart would need to express the same dynamic logic to remain
  equivalent — Helm templates cannot do this, so standalone would diverge.

**Why not have the controller deploy itself?**

On OLM clusters, OLM owns the CRDs and the operator lifecycle. The controller is an
operator workload — its Deployment and RBAC should be managed by the umbrella operator,
following the same pattern as Authorino and DNS-operator in the kuadrant-operator POC.
This keeps all operator workload lifecycle in one place (kuadrant-operator) and lets OLM
manage upgrades, health checks, and RBAC ownership consistently.

---

## ClusterRole ownership

There are two distinct ClusterRoles involved, owned and created by different actors.

### 1. kuadrant-operator's own ClusterRole (`manager-role`)

**Who needs it:** kuadrant-operator itself (`kuadrant-operator-controller-manager` ServiceAccount).

**Who creates it:** OLM, from the CSV `clusterPermissions` section on operator install.

**What it grants:** everything kuadrant-operator needs to do its job across all components,
including the permissions to SSA-apply the mcp-gateway controller chart resources:

| Resource | Why needed |
|---|---|
| `apps/deployments` | SSA-apply the mcp-gateway controller Deployment |
| `serviceaccounts` | SSA-apply the mcp-gateway controller ServiceAccount |
| `rbac.authorization.k8s.io/clusterroles` | SSA-apply the mcp-gateway controller ClusterRole |
| `rbac.authorization.k8s.io/clusterrolebindings` | SSA-apply the mcp-gateway controller ClusterRoleBinding |
| `mcp.kuadrant.io/mcpgatewayextensions` (get, list, watch) | Watch extensions in the SOTW topology |

`clusterroles` and `clusterrolebindings` are needed because kuadrant-operator SSA-applies
both when rendering `charts/mcp-gateway`. Without `clusterroles`, the apply returns 403 and
the entire chart render fails.

**Source:** `config/rbac/role.yaml` → generated into bundle CSV `clusterPermissions`.

---

### 2. mcp-gateway controller's ClusterRole (`kuadrant-operator-mcp-gateway-controller`)

**Who needs it:** the mcp-gateway controller binary (`kuadrant-operator-mcp-gateway-controller` ServiceAccount).

**Who creates it (OLM / umbrella):** OLM, from the static file
`bundle/manifests/kuadrant-operator-mcp-gateway-controller-role_rbac.authorization.k8s.io_v1_clusterrole.yaml`
on operator install. This follows the same pattern as Authorino
(`kuadrant-operator-authorino-manager-role_...clusterrole.yaml`) and DNS-operator.

**Who creates it (standalone Helm):** Helm, from `charts/mcp-gateway/templates/rbac.yaml`
during `helm install`.

**What it grants:** everything the mcp-gateway controller needs to watch and manage
resources across the cluster:

| Resource | Why needed |
|---|---|
| `mcp.kuadrant.io/mcpgatewayextensions` | Watch, update status and finalizers |
| `mcp.kuadrant.io/mcpserverregistrations` | Watch, update status |
| `mcp.kuadrant.io/mcpvirtualservers` | Watch, update status and finalizers |
| `apps/deployments` | Create/update broker-router Deployment per extension |
| `core/secrets`, `core/serviceaccounts`, `core/services` | Create broker-router SA, session key, config and trusted-headers secrets, broker-router Service |
| `gateway.networking.k8s.io/gateways` + status | Read target Gateway, write listener status |
| `gateway.networking.k8s.io/httproutes` + status | Create broker-router HTTPRoutes, update route status for MCPServerRegistration |
| `gateway.networking.k8s.io/referencegrants` | Validate cross-namespace Gateway references |
| `networking.istio.io/envoyfilters` | Create/update/delete EnvoyFilter in gateway namespace |
| `core/namespaces` | Validate namespace conflicts |

**Note:** the ClusterRoleBinding that binds this ClusterRole to the ServiceAccount is
**not** in the bundle — it is always rendered by the Helm chart (`charts/mcp-gateway/templates/rbac.yaml`)
and SSA-applied by kuadrant-operator at runtime. This is because the binding needs the
namespace where the ServiceAccount lives, which is a runtime value (`operatorNamespace`),
not a static one. This is the same split used for Authorino and DNS-operator in the POC.

---

### ClusterRole lifecycle summary

```
OLM install
│
├── Creates kuadrant-operator ClusterRole (manager-role)
│     bound to: kuadrant-operator-controller-manager ServiceAccount
│     grants: SSA apply of deployments, serviceaccounts, clusterroles,
│             clusterrolebindings, mcp.kuadrant.io watch
│
└── Creates mcp-gateway controller ClusterRole
      (kuadrant-operator-mcp-gateway-controller)
      bound to: (not yet — binding applied at runtime by kuadrant-operator)
      grants: full mcp-gateway domain permissions

kuadrant-operator starts (using manager-role)
│
└── MCPGatewayReconciler.reconcileController() SSA-applies charts/mcp-gateway:
      Creates ServiceAccount (kuadrant-operator-mcp-gateway-controller)
      SSA-applies ClusterRole  ← OLM already owns this; SSA merges managed fields
      Creates ClusterRoleBinding ← binds controller ClusterRole to ServiceAccount
      Creates Deployment (mcp-controller)

mcp-controller starts (using mcp-gateway controller ClusterRole)
│
└── Watches MCPGatewayExtension, creates broker-router resources per extension
```

---

## Helm rendering implementation (kuadrant-operator)

`pkg/helm.Renderer` renders `charts/mcp-gateway` with:

- `ClientOnly: true`, `DryRun: true` — no cluster contact during render
- `SkipCRDs: true` — CRDs omitted; OLM installs them from bundle
- `DisableHooks: true` — test Pods excluded from rendered output

Each rendered object is applied via dynamic client SSA:

```go
dynamicClient.Resource(gvr).Namespace(ns).Apply(ctx, name, obj,
    metav1.ApplyOptions{FieldManager: "kuadrant-operator", Force: true})
```

GVR is resolved from Kind via `kindToResource()` — a switch statement with explicit cases
for all resource kinds the chart renders. The REST mapper would be more robust for
production but requires passing it into the reconciler.

---

## CRD ownership summary

| Installation | Who installs CRDs | Who owns CRDs at runtime |
|---|---|---|
| Standalone (Helm) | Helm `crds/` dir on `helm install` | Helm (not tracked as release resource — Helm does not delete CRDs on uninstall by design) |
| OLM (kuadrant-operator bundle) | OLM on operator install, from `bundle/manifests/mcp.kuadrant.io_*.yaml` | OLM / CSV (manages CRD lifecycle with bundle version) |
| Non-OLM managed cluster (Any:Kube) | Open gap — not solved in current POC | TBD |

---

## Alignment with olmv1-umbrella-poc

The `olmv1-umbrella-poc` branch of kuadrant-operator establishes the Helm rendering pattern
we follow. It deploys Authorino, Limitador, and DNS-operator directly via Helm charts
rather than through their own separate operators.

### How the POC handles Authorino

The POC runs **two** Authorino reconcilers simultaneously — this is a transitional WIP:

- **`workflow.authorino`** (`NewAuthorinoReconciler`) — the old pattern. Only runs when
  `isAuthorinoOperatorInstalled=true`. kuadrant-operator creates an `Authorino` CR, the
  authorino-operator watches it and deploys Authorino.
- **`workflow.helm_authorino`** (`NewHelmAuthorinoReconciler`) — the new pattern. Runs
  only when MCPGateway CRD is detected. kuadrant-operator renders `charts/authorino`
  directly and deploys Authorino itself — no authorino-operator needed.

The intent is to eventually drop `workflow.authorino` and the authorino-operator dependency
entirely once the Helm approach is proven. For mcp-gateway there is no old pattern to
replace — we go straight to the Helm approach.

### What each Helm reconciler deploys

| Component | ServiceAccount | ClusterRole | ClusterRoleBinding | Deployment | Service | ConfigMap |
|---|---|---|---|---|---|---|
| Authorino | yes | yes (OLM-owned) | yes (chart) | yes | yes (x2) | — |
| DNS-operator | yes | yes (OLM-owned) | yes (chart) | yes | — | yes |
| mcp-gateway controller | yes | yes (OLM-owned) | yes (chart) | yes | — | — |

ClusterRole: created by OLM from bundle manifest, SSA-applied by kuadrant-operator (merges managed fields).
ClusterRoleBinding: always rendered by Helm chart and applied by kuadrant-operator (namespace is a runtime value).

### Shared infrastructure

All reconcilers use the same utilities:

- `pkg/helm.Renderer` — `SkipCRDs: true`, `ClientOnly: true`, `DryRun: true`
- Dynamic client SSA with `FieldManager: "kuadrant-operator"`, `Force: true`
- `kindToResource()` for GVR resolution — explicit switch, fragile default fallback for unknown kinds (REST mapper is the correct long-term fix)
- `isClusterScoped()` — routes ClusterRole/ClusterRoleBinding to cluster-scoped resource client
- `FieldManagerName = "kuadrant-operator"` from `common.go`

### Key difference for mcp-gateway

For Authorino and DNS-operator, kuadrant-operator deploys the operator workload and that
operator manages its domain resources (AuthConfigs, DNS records) independently.

For mcp-gateway, kuadrant-operator deploys the controller workload. The controller then
manages all per-MCPGatewayExtension resources (broker-router Deployment, Service,
ServiceAccount, HTTPRoutes, secrets, EnvoyFilter) identically to standalone mode. No
domain logic moves to kuadrant-operator.

---

## Key files

| File | Repo | Purpose |
|---|---|---|
| `charts/mcp-gateway/` | mcp-gateway | Controller chart — rendered by kuadrant-operator in OLM mode, installed by Helm in standalone |
| `charts/mcp-gateway/crds/` | mcp-gateway | CRDs for standalone Helm install |
| `pkg/helm/renderer.go` | kuadrant-operator | Helm rendering utility (`SkipCRDs=true`, returns `[]*unstructured.Unstructured`) |
| `internal/controller/mcpgateway_reconciler.go` | kuadrant-operator | `MCPGatewayReconciler` — renders controller chart and applies via dynamic client SSA |
| `internal/controller/state_of_the_world.go` | kuadrant-operator | CRD detection, MCPGatewayExtension watcher, reconciler subscription wiring |
| `internal/controller/mcpgatewayextension_controller.go` | mcp-gateway | Full extension reconciler — broker-router, secrets, EnvoyFilter, validation, status |

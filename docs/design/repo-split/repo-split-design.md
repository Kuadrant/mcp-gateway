# Repository Split: Operator and Operand

**Status:** Proposed
**Issue:** [#1020](https://github.com/Kuadrant/mcp-gateway/issues/1020)
**Jira:** [CONNLINK-1025](https://redhat.atlassian.net/browse/CONNLINK-1025)

## Summary

Split the mcp-gateway monorepo into two independent repositories to enable separate release cadences, build pipelines, and versioning for the operator (control-plane) and operand (data-plane).

## Motivation

The current monorepo couples the operator and operand release cycles. Changes to the broker or router require rebuilding the controller image and vice versa. Separate repos enable:

- Independent versioning and release cadences
- Smaller, faster CI pipelines per repo
- Clearer ownership boundaries
- Alignment with existing Kuadrant patterns (`authorino`/`authorino-operator`, `limitador`/`limitador-operator`)

## Design

### Repository Boundary

Two repositories with no compile-time Go dependencies between them:

| | `mcp-gateway` (operand) | `mcp-gateway-operator` (new) |
|---|---|---|
| Purpose | Data-plane: broker + router | Control-plane: controller + operator |
| Binary | `cmd/mcp-broker-router/main.go` | `cmd/main.go` |
| Image | `ghcr.io/kuadrant/mcp-gateway` | `ghcr.io/kuadrant/mcp-controller` |
| CRDs | None | All (MCPGatewayExtension, MCPServerRegistration, MCPVirtualServer) |
| Helm chart | None (deployed by operator) | `charts/mcp-gateway/` |
| OLM bundle | None | `bundle/` |

The `mcp-gateway` repo keeps its current name. The operator moves to a new `mcp-gateway-operator` repo. This follows the established Kuadrant convention.

### Repository Structure

#### `mcp-gateway` (operand)

```text
mcp-gateway/
├── cmd/mcp-broker-router/main.go
├── internal/
│   ├── broker/          # MCP broker + status endpoint
│   ├── mcp-router/      # Envoy ext_proc server
│   ├── config/          # config types + runtime reader (Observer, Viper)
│   ├── session/         # JWT manager, session cache
│   ├── clients/         # HTTP client for hairpin initialize
│   ├── elicitation/     # URL token elicitation
│   ├── idmap/           # Redis-backed ID mapping
│   ├── otel/            # OpenTelemetry instrumentation
│   └── tests/           # shared test utilities
├── tests/
│   ├── servers/         # test MCP server source code
│   └── perf/            # load testing
├── Dockerfile
└── go.mod               # github.com/Kuadrant/mcp-gateway
```

#### `mcp-gateway-operator` (new repo)

```text
mcp-gateway-operator/
├── cmd/main.go
├── api/v1alpha1/        # CRD types
├── internal/
│   ├── controller/      # all reconcilers + server validator
│   └── config/          # config secret writer + serialization types
├── charts/mcp-gateway/  # Helm chart (CRDs, RBAC, controller, MCPGatewayExtension)
├── bundle/              # OLM manifests
├── catalog/             # FBC catalog
├── config/
│   ├── crd/             # generated CRD YAMLs
│   ├── mcp-system/      # K8s deployment manifests
│   ├── rbac/            # RBAC manifests
│   ├── test-servers/    # K8s manifest for a minimal test server
│   └── samples/         # example manifests
├── tests/
│   └── e2e/             # controller-focused e2e (envtest + minimal server)
├── Dockerfile
└── go.mod               # github.com/Kuadrant/mcp-gateway-operator
```

### Operator-Operand Contract

The two repos communicate through runtime contracts only — no shared Go modules.

#### Config Secret (`mcp-gateway-config`)

The operator writes a Kubernetes Secret containing `config.yaml`. The operand reads it via volume mount at `/config/config.yaml`.

```yaml
configVersion: 1
servers:
  - name: string
    url: string
    hostname: string                # optional
    prefix: string                  # optional
    auth:                           # optional
      type: string
      token: string
      username: string
      password: string
    credential: string              # optional, env var name
    caCert: string                  # optional, PEM CA certificate
    state: string                   # optional, "Enabled" (default) or "Disabled"
    tokenURLElicitation:            # optional
      url: string
    userSpecificList: bool          # optional
    category: [string]             # optional
    hint: string                   # optional
    tags: [string]                 # optional
virtualServers:                     # optional
  - name: string
    tools: [string]
    prompts: [string]
```

Both repos define their own Go structs for this schema. The operator serializes it; the operand deserializes it. Unknown fields are ignored by the operand, enabling forward compatibility.

**Schema evolution rules:**
- The operand must ignore unknown fields (forward compatibility).
- The operator must not remove a field without a deprecation period spanning at least one operand release, preventing older operands from receiving a config missing a field they expect.
- New required fields must have sensible defaults so older operators that don't set them still produce valid config.
- Breaking changes require a coordinated release with both repos tagged.
- The config includes a top-level `configVersion: 1` field. This is a schema version, not tied to the operator or operand release version — it is bumped only when the config secret format has a breaking structural change (field renames, type changes, removed fields). Additive changes (new optional fields) do not bump the version. The operand checks this on load — known versions are processed normally; unknown versions log a warning with the expected and actual values.

#### Status Endpoint (`GET /status`)

The operator calls `http://mcp-gateway.{ns}.svc.cluster.local:8080/status` and parses the JSON response. The operator defines its own minimal response structs, consuming only the fields it needs:

```go
// operator repo — internal/controller/status_types.go
type StatusResponse struct {
    Servers []ServerStatus `json:"servers"`
}

type ServerStatus struct {
    ID         string `json:"id"`
    Ready      bool   `json:"ready"`
    Message    string `json:"message"`
    TotalTools int    `json:"totalTools"`
}
```

The operand's actual `/status` response includes additional fields (connection status, protocol validation, capabilities, tool conflicts) which the operator ignores.

#### Deployment Contract

The operator controls the operand's deployment specification:

| Contract | Type | Details |
|---|---|---|
| Image | `RELATED_IMAGE_ROUTER_BROKER` | Env var on operator, defaults to `ghcr.io/kuadrant/mcp-gateway:latest` |
| CLI flags | Stable set | `--mcp-broker-public-address`, `--mcp-gateway-private-host`, `--mcp-gateway-config`, `--mcp-check-interval`, `--mcp-gateway-public-host`, `--enable-url-elicitation` |
| Env vars | Stable set | `JWT_SESSION_SIGNING_KEY`, `TRUSTED_HEADER_PUBLIC_KEY`, `CACHE_CONNECTION_STRING` |
| Ports | Fixed | 8080 (HTTP), 50051 (gRPC), 8181 (config) |
| Health | HTTP | `GET /readyz` on port 8080 |
| Config mount | Volume | `/config/config.yaml` |

### CI/CD Pipeline Split

#### Testing Strategy

Most feature work lands in the operand repo (broker/router), not the operator. The testing strategy reflects this — each repo tests its own component independently, and a cross-repo job catches version skew.

**Operand repo — tests the gateway as a gateway.** Deploys the gateway directly via Helm (no operator in the loop), creates config secrets with the expected config, sets the needed env vars and flags, and runs e2e tests against the gateway's MCP functionality. This is where most e2e coverage lives since most features land here. The operand repo also runs unit tests, conformance tests, and lint.

**Operator repo — tests the controller as a controller.** envtest integration tests prove the controller produces correct Deployments, Secrets, and RBAC from CRs. Unit tests cover reconciliation logic. No real gateway needed. The operator repo also runs Helm install smoke tests and CRD sync verification.

**Cross-repo compatibility job — the integration point.** A manually triggered (and nightly) workflow that deploys version X of the operator with version Y of the operand together. Catches version skew between the two components. Required gate before any release.

#### `mcp-gateway` (operand) CI

| Workflow | Trigger | Purpose |
|---|---|---|
| `tests.yaml` | PR, push to main | Unit tests for broker, router, session, config |
| `e2e.yaml` | PR, push to main | Gateway e2e via Helm (no operator), config secret + flags |
| `code-style.yaml` | PR | Lint, vet, formatting |
| `images.yaml` | Push to main, tags | Build + push `ghcr.io/kuadrant/mcp-gateway` |
| `test-images.yaml` | Push to main | Build + push test server images |
| `conformance.yaml` | Push to main | MCP protocol conformance tests |
| `spelling.yaml` | PR | Spell check |

#### `mcp-gateway-operator` CI

| Workflow | Trigger | Purpose |
|---|---|---|
| `tests.yaml` | PR, push to main | Unit tests for controllers |
| `controller-integration-tests.yaml` | PR, push to main | envtest integration tests |
| `code-style.yaml` | PR | Lint, vet, formatting |
| `images.yaml` | Push to main, tags | Build + push `ghcr.io/kuadrant/mcp-controller` |
| `helm-release.yaml` | Tags | Publish Helm chart |
| `helm-install-test.yaml` | PR | Helm install smoke test |
| `verify-crd-sync.yaml` | PR | Ensure generated CRDs match chart CRDs |

#### Cross-Repo Compatibility

A workflow in the operator repo that deploys both components together and runs full integration tests:

- **Nightly:** Builds `:nightly` tagged images from main of both repos and tests them together. Catches drift between the two codebases.
- **Manual:** Accepts image overrides for both components. Used for custom combinations — e.g., running `rc1` of the operand with `v1.5` of the operator when there were no operator changes.
- **Pre-release gate:** Must pass before tagging either component.

#### Release Flow

```text
Operand release:
  1. Tag mcp-gateway → CI builds + pushes image
  2. Operator repo bumps RELATED_IMAGE_ROUTER_BROKER default

Operator release:
  1. Tag mcp-gateway-operator → CI builds image + publishes Helm chart
  2. Helm chart references the operand image tag from the bump above
```

Independent release cadences. The operator pins to a known-good operand version. Compatibility is documented in a version matrix in the operator repo README.

### OLM and Release Pipeline

CI is GitHub Actions-based — no Tekton pipelines to split. The OLM packaging artifacts (`bundle/`, `catalog/`, `bundle.Dockerfile`, `build/olm.mk`, `utils/generate-catalog.sh`) and Konflux ReleasePlan resources all move to the operator repo. The operand repo has no OLM footprint.

Post-split, a new Konflux component and product build repository are needed for the operator (similar to `rh-api-management/mcp-gateway-product-build`). The existing product build repo is reworked to be operand-specific. Each repo gets its own Konflux component, ReleasePlan, and release pipeline. The FBC catalog generation (`utils/generate-catalog.sh`) continues to run from the operator repo since the CSV and CRDs live there.

## Migration Plan

Three phases, each non-breaking. At no point is either repo in a broken state.

### Phase 1: Decouple in Monorepo

Remove all compile-time Go imports between operator and operand code within the current repo.

**Step 1: Decouple controller → broker types.**
Create `internal/controller/status_types.go` with minimal `StatusResponse` and `ServerStatus` structs. Update `server_validator.go` to use these instead of importing `internal/broker` and `internal/broker/upstream`. The controller currently accesses only 4 fields: `.ID`, `.Ready`, `.Message`, `.TotalTools`.

**Step 2: Decouple controller → config package.**
Move `config_writer.go` in its entirety into `internal/controller/` — this includes `SecretReaderWriter` and all its methods (`UpsertMCPServer`, `RemoveMCPServer`, `WriteEmptyConfig`, `EnsureConfigExists`, `WriteVirtualServerConfig`), plus `NamespaceName()` and `DefaultNamespaceName`. Copy the serialization-side types (`BrokerConfig`, `MCPServer`, `AuthConfig`, `VirtualServerConfig`, `TokenURLElicitationConfig`) from `types.go` into the same package. Move `config_writer_test.go` alongside. Update `cmd/main.go` (controller entry point) which also directly imports `config.SecretReaderWriter`. Remove all controller imports of `internal/config/`.

The `internal/config/` package itself stays in the operand repo — it contains the runtime config types (`MCPServersConfig`, `Observer` interface), the config watcher, and runtime accessors used by the broker and router. Only the serialization-side types needed for writing the config secret are copied to the controller.

**Step 3: Remove operand → api dependency.**
The operand imports `api/v1alpha1` for `InvalidToolPolicy` (a typed string enum with two constants) and `ServerStateEnabled` (a string constant). These are used across 5 files: `cmd/mcp-broker-router/broker.go`, `internal/broker/broker.go`, `internal/broker/user_specific_tools.go`, `internal/broker/upstream/manager.go`, and `internal/broker/upstream/mcp.go`. Define a local `InvalidToolPolicy` type with `FilterOut`/`RejectServer` constants and a `ServerStateEnabled` constant in a new `internal/broker/policy.go` file. Update all 5 files to use the local definitions.

**Step 4: Verify independence.**
Add a CI check that builds each binary (`cmd/main.go` and `cmd/mcp-broker-router/main.go`) in isolation, confirming zero cross-imports.

### Phase 2: Create Operator Repo

1. Create `Kuadrant/mcp-gateway-operator` with standard Kuadrant repo setup.

2. Copy operator code (see "What moves" table below). This is a file copy, not `git filter-branch` — git history stays in the monorepo.

3. Set up `go.mod` as `github.com/Kuadrant/mcp-gateway-operator`. No import of the operand module.

4. Set up CI workflows — copy and adapt from the monorepo.

5. Verify: operator repo builds, tests pass, Helm chart installs, e2e passes against a released operand image.

### Phase 3: Clean Up Operand Repo

1. Remove operator code from `mcp-gateway`: `cmd/main.go`, `api/`, `internal/controller/`, `Dockerfile.controller`, `charts/`, `bundle/`, `config/crd/`, `config/mcp-system/`, `config/test-servers/`, `config/samples/`, `tests/e2e/`, operator-specific CI workflows.

2. Optionally move `cmd/mcp-broker-router/main.go` to `cmd/main.go`.

3. Clean up `go.mod` — remove controller-runtime, gateway-api, and other operator-only dependencies.

4. Update README and docs to point to the operator repo.

### What Moves, What Stays

| Item | Destination |
|---|---|
| `cmd/main.go` (controller binary) | mcp-gateway-operator |
| `cmd/mcp-broker-router/main.go` | stays in mcp-gateway |
| `api/v1alpha1/` | mcp-gateway-operator |
| `internal/controller/` | mcp-gateway-operator |
| `internal/config/config_writer.go` | mcp-gateway-operator (with local types) |
| `internal/config/types.go` | both repos get their own copy |
| `internal/broker/`, `mcp-router/`, `session/`, etc. | stays in mcp-gateway |
| `charts/`, `bundle/`, `catalog/` | mcp-gateway-operator |
| `bundle.Dockerfile`, `build/olm.mk`, `utils/generate-catalog.sh` | mcp-gateway-operator |
| `config/crd/`, `config/mcp-system/` | mcp-gateway-operator |
| `config/test-servers/` | mcp-gateway-operator (minimal — one server for MCPServerRegistration testing) |
| `tests/e2e/` | mcp-gateway-operator |
| `tests/servers/` (source code) | stays in mcp-gateway |
| `Dockerfile` (operand) | stays in mcp-gateway |
| `Dockerfile.controller` | mcp-gateway-operator (rename to `Dockerfile`) |
| `docs/guides/`, `docs/reference/` | mcp-gateway-operator (update docs.kuadrant.io nav config) |
| `docs/design/` (CRD semantics, reconciliation, deployment) | mcp-gateway-operator |
| `docs/design/` (broker/router internals, routing, sessions, performance) | stays in mcp-gateway |
| `Makefile` | split — each repo gets its own (see below) |
| `CLAUDE.md` | split — each repo gets its own scoped version |
| `internal/config/config_writer_test.go` | mcp-gateway-operator |

## Additional Artifacts

### Makefile

The current `Makefile` has targets for both components (`build`, `docker-build`, `docker-build-controller`, `generate`, `manifests`, etc.). Post-split:

- **Operator repo** gets a new `Makefile` with controller-specific targets: `generate`, `manifests`, `docker-build`, `test-unit`, `test-controller-integration`, `lint`, `helm-*`, and CRD generation (`controller-gen`). Includes `make deploy-operator OPERAND_IMAGE=<image>:<tag>` to deploy the operator with a specific operand version — used for local development and cross-repo testing.
- **Operand repo** retains the existing `Makefile` but removes controller-specific targets (`docker-build-controller`, `generate`, `manifests`, CRD generation). Keeps `build`, `docker-build`, `test-unit`, `lint`, and performance test targets.

### CLAUDE.md

The root `CLAUDE.md` documents both components. Post-split, each repo gets its own `CLAUDE.md` scoped to its component:

- **Operator repo** `CLAUDE.md`: CRD types, controller reconciliation, config secret writing, Helm chart, OLM bundle, operator deployment.
- **Operand repo** `CLAUDE.md`: broker, router, session management, upstream MCP connections, ext_proc protocol, performance guidelines.

### docs.kuadrant.io

User-facing guides are published at docs.kuadrant.io from the `Kuadrant/docs.kuadrant.io` repo. The nav config in `mkdocs.yml` references files from the MCP Gateway section. Post-split, these references need updating to point at the operator repo since guides and CRD reference docs move there. This update should be part of Phase 2 step 4 (CI setup). No redirects are needed — the docs repo pulls files by path from a configured repo, so changing the source repo in `mkdocs.yml` is sufficient.

### Test Server Images

Test server source code (`tests/servers/`) stays in the operand repo and produces test server container images. The operator repo only needs a single minimal test server and its K8s manifest to test MCPServerRegistration reconciliation — it does not need the full suite of test servers. The operand repo's e2e tests use the full set of test servers directly.

## Current Coupling Analysis

The following compile-time coupling points must be resolved in Phase 1. There are two directions: controller code importing operand packages (points 1-6), and operand code importing CRD types (points 7-10).

**Controller → operand packages:**

| Coupling | Files | What the controller uses |
|---|---|---|
| `broker.StatusResponse` | `internal/controller/server_validator.go` → `internal/broker/status.go` | JSON response struct |
| `upstream.ServerValidationStatus` | `internal/controller/mcpserverregistration_controller.go` → `internal/broker/upstream/manager.go` | `.ID`, `.Ready`, `.Message`, `.TotalTools` fields |
| `config.SecretReaderWriter` | `internal/controller/` → `internal/config/config_writer.go` | Config secret read/write |
| `config.MCPServer` et al. | `internal/controller/` → `internal/config/types.go` | Serialization types |
| `config.SecretReaderWriter` | `cmd/main.go` (controller entry point) → `internal/config/config_writer.go` | Direct instantiation |

**Operand → CRD types (`api/v1alpha1/`):**

| Coupling | Files | What the operand uses |
|---|---|---|
| `mcpv1alpha1.InvalidToolPolicy` | `cmd/mcp-broker-router/broker.go` → `api/v1alpha1/` | Type cast + constant validation |
| `mcpv1alpha1.InvalidToolPolicy` | `internal/broker/broker.go` → `api/v1alpha1/` | Type for struct field + `WithInvalidToolPolicy` option |
| `mcpv1alpha1.InvalidToolPolicy*` | `internal/broker/user_specific_tools.go` → `api/v1alpha1/` | `FilterOut` and `RejectServer` constants in switch |
| `mcpv1alpha1.InvalidToolPolicy*` | `internal/broker/upstream/manager.go` → `api/v1alpha1/` | Type for struct field + `RejectServer` constant |
| `mcpv1alpha1.ServerStateEnabled` | `internal/broker/upstream/mcp.go` → `api/v1alpha1/` | String constant for enabled check |

All ten coupling points are narrow and straightforward to resolve.

## Kuadrant Precedent

This design follows the established Kuadrant operator/operand pattern:

| Aspect | `authorino` / `authorino-operator` | `limitador` / `limitador-operator` | `mcp-gateway` / `mcp-gateway-operator` |
|---|---|---|---|
| Operator imports operand? | No | No | No |
| Operand image reference | `RELATED_IMAGE_AUTHORINO` | `RELATED_IMAGE_LIMITADOR` | `RELATED_IMAGE_ROUTER_BROKER` |
| CRDs owned by | Operator | Operator | Operator |
| Helm chart in | Operator | Operator | Operator |
| OLM bundle in | Operator | Operator | Operator |

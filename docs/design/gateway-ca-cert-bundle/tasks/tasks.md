# Gateway CA Certificate Bundle — Implementation Plan

## Context

The MCP Gateway's custom TLS support (#659, #1008) embeds per-server CA PEM data inline in the config secret. When many servers share the same CA, this causes duplication, config bloat, and operational overhead. This feature adds a `caCertBundleRef` field to `MCPGatewayExtension` for a shared trust pool.

Design: `docs/design/gateway-ca-cert-bundle/gateway-ca-cert-bundle-design.md`

## Existing Code to Build On

### Per-server CA cert flow (already implemented)
1. **CRD field**: `MCPServerRegistrationSpec.CACertSecretRef` (`api/v1alpha1/types.go:88-93`)
2. **Type**: `CACertSecretReference` with `Name` and `Key` (default `ca.crt`) (`api/v1alpha1/types.go:165-177`)
3. **Controller**: reads secret via `DirectAPIReader`, validates label/key/size/PEM, writes inline to config (`mcpserverregistration_controller.go:447-479`)
4. **Config type**: `MCPServer.CACert string` (`internal/config/types.go:97`)
5. **Broker upstream**: `buildHTTPClient()` creates custom `*http.Client` with CA appended to system roots (`internal/broker/upstream/mcp.go:46-69`)
6. **Broker connect**: passes custom client via `transport.WithHTTPBasicClient()` (`internal/broker/upstream/mcp.go:140-146`)

### Config secret flow
- `BrokerConfig` in `internal/config/types.go:169-172` — holds servers + virtual servers, serialized as `config.yaml` in `mcp-gateway-config` Secret
- `SecretReaderWriter` in `internal/config/config_writer.go` — read-modify-write pattern with retry on conflict
- `mcpConfig.Notify()` — observer pattern that notifies broker of config changes

### MCPGatewayExtension controller
- `reconcileActive()` in `mcpgatewayextension_controller.go` — orchestrates Deployment, Service, HTTPRoutes, EnvoyFilter
- `reconcileBrokerRouter()` — creates/updates the broker-router Deployment with volumes, env vars, and args

## Implementation Order

Tasks are ordered by dependency.

### Task 1: CRD + config types

**Files:**
- `api/v1alpha1/mcpgatewayextension_types.go` — add `CACertBundleRef *CACertBundleReference` to `MCPGatewayExtensionSpec`
- `api/v1alpha1/mcpgatewayextension_types.go` — add `CACertBundleReference` struct with `Name` and `Key` (default `ca.crt`)
- `internal/config/types.go` — add `GatewayCACertPEM string` to `BrokerConfig`
- Run `make generate-all` to regenerate deepcopy, CRDs, sync Helm

**Acceptance criteria:**
- [ ] CRD accepts `caCertBundleRef` with `name` (required) and `key` (optional, default `ca.crt`)
- [ ] `BrokerConfig` has `GatewayCACertPEM` field serialized as `gatewayCACertPEM` in YAML
- [ ] `make generate-all && make lint` passes
- [ ] Existing tests pass without changes

**Verification:** `make generate-all && make lint && make test-unit`

---

### Task 2: MCPGatewayExtension controller — validation + config write

Depends on: Task 1.

**Files:**
- `internal/controller/mcpgatewayextension_controller.go` — add `reconcileCACertBundle()`:
  1. If `caCertBundleRef` is nil, skip (clear any existing `GatewayCACertPEM` from config)
  2. Read the referenced Secret via `DirectAPIReader`
  3. Validate: exists, has `mcp.kuadrant.io/secret=true` label, key exists, size ≤ 256 KiB, valid PEM
  4. Write CA PEM into `config.yaml` under `gatewayCACertPEM` key via `SecretReaderWriter`
  5. Set status condition on error
- `internal/controller/mcpgatewayextension_controller.go` — add Secret watch for labeled Secrets (already exists for MCPServerRegistration, extend to MCPGatewayExtension)
- `internal/controller/mcpgatewayextension_controller_test.go` — unit tests

**Acceptance criteria:**
- [ ] Valid CA bundle secret → `gatewayCACertPEM` written to config, status Ready
- [ ] Missing secret → status condition with error message
- [ ] Missing label → status condition with error message
- [ ] Invalid PEM → status condition with error message
- [ ] Size exceeds 256 KiB → status condition with error message
- [ ] Secret update triggers re-reconciliation and config update
- [ ] Config update flows through `mcpConfig.Notify()` for observer synchronization
- [ ] No `caCertBundleRef` → no `gatewayCACertPEM` in config (backward compatible)
- [ ] Integration test: `gatewayCACertPEM` present in config secret when bundle is set, absent when not set

**Verification:** `make lint && make test-unit && make test-controller-integration`

---

### Task 3: Broker — read gateway CA bundle from config + build base trust pool

Depends on: Task 2.

**Files:**
- `internal/broker/broker.go` — in `OnConfigChange()`:
  1. Read `GatewayCACertPEM` from config
  2. If non-empty, build base `*x509.CertPool` from system roots + gateway CA
  3. Store as shared base pool for all server connections
- `internal/broker/upstream/mcp.go` — modify `buildHTTPClient()`:
  1. Accept an optional base `*x509.CertPool` parameter (the gateway CA bundle pool)
  2. Clone the base pool (or system roots if nil)
  3. Append per-server `CACert` if set
  4. Return custom client with combined pool
- `internal/broker/upstream/mcp_test.go` — unit tests for combined pool behavior

**Acceptance criteria:**
- [ ] Broker reads `GatewayCACertPEM` from config on change
- [ ] `buildHTTPClient()` uses base pool when available
- [ ] Per-server `CACert` appends to base pool (not replaces)
- [ ] No `GatewayCACertPEM` in config → behavior identical to current (system roots only)
- [ ] Invalid PEM in config → broker logs error, falls back to system roots

**Verification:** `make test-unit`

---

### Task 4: Documentation and guide updates

Depends on: Tasks 1–3.

**Files:**
- `docs/guides/custom-ca-certificates.md` — add section for gateway-level CA bundle
- `docs/reference/mcpgatewayextension.md` — add `caCertBundleRef` field documentation
- `AGENTS.md` — update with `caCertBundleRef` information

**Acceptance criteria:**
- [ ] Guide covers: when to use gateway bundle vs per-server CA, step-by-step setup, rotation
- [ ] API reference updated with field, type, default, constraints
- [ ] AGENTS.md updated

---

### Task 5: E2E tests

Depends on: Tasks 1–3.

**Files:**
- `tests/e2e/ca_bundle_test.go` (new) — E2E test cases

**Acceptance criteria:**
- [ ] All e2e test cases from `e2e_test_cases.md` pass
- [ ] Tests clean up resources after each case

**Verification:** `make test-e2e`

---

## Verification (full)

```bash
make generate-all       # CRD regeneration
make lint               # Style checks
make test-unit          # All unit tests
make test-controller-integration  # Controller integration tests
make test-e2e           # E2E with Kind cluster
```

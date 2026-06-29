# CI: E2E Test Suites

## Overview

E2E specs use Ginkgo `Label()` annotations for suite membership. The suite system provides tiered coverage: a fast PR gate, broader on-demand coverage, and named functional suites for targeted testing.

## Suite Reference

| Suite | Area | Extra infra |
|-|-|-|
| `core` | registration, tools/list, tools/call, unregister, notifications | - |
| `routing` | HTTPRoute, Gateway listener, Hostname backendRef | - |
| `sessions` | session rewrite, lazy init, concurrent sessions, Redis persistence | - |
| `discovery` | discover_tools, select_tools, category/hint, thresholds | - |
| `prompts` | prompts/list, prompts/get, aggregation and filtering | - |
| `auth-policy` | Authorino/Keycloak AuthPolicy coverage | Keycloak |
| `trusted-headers` | x-mcp-authorized, x-mcp-virtualserver, JWT filtering | - |
| `elicitation` | MCP elicitation request/response | - |
| `url-elicitation` | URL token elicitation, submit, cache, invalidation | - |
| `user-specific-list` | per-user tools/list and routing | - |
| `tls` | internal TLS backend, CA/DestinationRule | cert-manager |
| `multi-gateway` | multiple MCPGatewayExtensions, listener isolation | extra resources |
| `security` | header stripping, negative/security boundary | - |

## Aggregate Targets

| Target | Specs | When |
|-|-|-|
| `pr` | ~31 specs (curated happy path) | every PR |
| `pr-extended` | ~61 specs (excludes Full/multi-gateway) | on demand |
| `full` | all specs | nightly, on demand |

## Make Targets

```bash
make test-e2e-pr                      # PR gate (~31 specs)
make test-e2e-pr-extended             # broader coverage (~61 specs)
make test-e2e-suite SUITE=discovery   # named suite
make test-e2e-ci-full                 # all specs
make test-e2e-list-suites             # list available suites
```

Local development:

```bash
make test-e2e-happy                   # pr suite, no cluster setup
make test-e2e-watch                   # watch mode
```

## CI Commands and Labels

Request on-demand runs via PR comment:

```text
/test-e2e discovery       # run a named suite
/test-e2e pr-extended     # broader coverage
/test-e2e full            # everything
```

Or apply a label: `e2e/discovery`, `e2e/pr-extended`, `e2e/full`.

## Labelling Specs

Every spec must have at least one suite label. Specs in the PR gate also carry the `pr` label.

```go
It("description", Label("core", "pr"), func() {
    // pr label includes it in the PR gate
    // core label assigns it to the core suite
})

It("description", Label("discovery"), func() {
    // no pr label -- only runs when discovery suite is requested
})
```

Bracket tags (`[Happy]`, `[Full]`) are preserved for backward compatibility but Ginkgo `Label()` is the primary mechanism.

## How the Suite Router Works

The suite router lives in `build/suite-router.sh`. When a `/test-e2e <suite>` comment or `e2e/<suite>` label is detected:

1. The workflow extracts the suite name from the comment or label
2. Known names (`pr`, `pr-extended`, `full`) map to aggregate Make targets
3. Named suites map to `make test-e2e-suite SUITE=<name>` which passes `--label-filter="<name>"` to Ginkgo
4. Unknown suite names are rejected with an error comment listing valid options

Valid suite names are defined in `E2E_KNOWN_SUITES` in `build/e2e.mk`.

## Suites Needing Extra Infrastructure

| Suite | Requirement | Setup |
|-|-|-|
| `auth-policy` | Keycloak | `make ci-auth-setup` |
| `tls` | cert-manager | `make ci-cert-manager-setup` |
| `multi-gateway` | additional gateway resources | deployed by test fixtures |

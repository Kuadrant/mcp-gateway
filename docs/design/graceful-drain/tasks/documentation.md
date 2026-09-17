# Graceful Drain — Documentation Plan

Design: [../graceful-drain-design.md](../graceful-drain-design.md)

Drain changes observable pod behaviour — pods take longer to terminate, and clients see a new retryable error during rollouts — so it needs user-facing documentation rather than design notes alone.

## User Guide (`docs/guides/`)

### When I want to deploy a new gateway version without disrupting users

When a platform engineer rolls out a gateway upgrade during working hours, they want to know what clients will experience so that they can decide whether a maintenance window is needed.

**Cover:**

- What a rollout looks like from the client's side: calls that fit inside the drain deadline complete, new sessions against the draining pod get a retryable error, the replacement pod is already serving
- The deadline boundary explicitly: a call that outlasts `drainDeadline` may lose its response when the bounded teardown begins. That is the contract, not a defect, and long-running tools should be sized against it
- Why `maxSurge` matters, and why node drain, eviction and crash are different from `kubectl rollout restart`
- That endpoint propagation is not something the gateway can bound; if a cluster is slow to converge, raising the propagation delay is the lever
- What is explicitly not promised: side-effecting tool calls whose response was lost, and streams already in progress

### When I want to understand why pods take longer to terminate

When a platform engineer notices termination taking tens of seconds after upgrading, they want to know whether that is expected so that they do not mistake a correct drain for a hung pod.

**Cover:**

- The termination sequence: `preStop` propagation delay, drain wait, bounded teardown
- How `terminationGracePeriodSeconds` is derived, and why it is computed rather than defaulted
- How to tell a slow drain from a stuck one

### When I want to observe drain behaviour

When a platform engineer investigates a slow rollout, they want the relevant signals so that they can diagnose it without reading pod logs.

**Cover:**

- Drain duration, requests completed during drain, forced terminations
- What a rising forced-termination count indicates, and what to do about it
- Why drain-window telemetry is exported at all, given that OTel shutdown ordering was previously wrong

### When my client sees errors during a deploy

When an MCP client developer sees failures coincide with a gateway rollout, they want to distinguish retryable gateway errors from upstream tool failures so that their retry logic does the right thing.

**Cover:**

- The exact contract: HTTP 503 with `Retry-After`, JSON-RPC error code -32000, and what the client should do with it
- Whether the official Go SDK retries this transparently or surfaces it — Task 8 establishes which, and the guidance is either "your client will retry" or "your client must retry"
- Why a side-effecting tool call whose response was lost cannot be retried safely by the gateway on the client's behalf
- Existing sessions are unaffected by drain; reconnection is not required

## Security Architecture (`docs/design/security-architecture.md`)

### When I want to confirm a draining pod cannot be exploited

When a security reviewer assesses the drain feature, they want assurance that a terminating pod cannot be induced into a weaker enforcement posture so that graceful shutdown does not become an attack window.

**Cover:**

- Draining changes when traffic stops arriving, never what happens to traffic that arrives; `failure_mode_allow` stays `false`
- The drain response is process-derived and cannot be induced by a client header
- No credential persistence is introduced, which is what kept durable cross-replica cleanup out of scope in #1363

## Release Notes (`docs/release-notes/`)

### When I want to know what changes for me on upgrade

When a platform engineer reads the release notes before upgrading, they want to know which observable behaviours change so that they can decide whether anything in their tooling or alerting needs adjusting first.

**Cover:**

- `terminationGracePeriodSeconds` is now set on the generated Deployment where it was previously defaulted — observable behaviour change, per `.claude/rules/breaking-changes.md`
- Pods take longer to terminate by design
- New retryable error during rollouts, and what clients should do with it

## Not required

No API reference changes: this iteration adds no CRD fields. If the budgets are later exposed on `MCPGatewayExtension`, `docs/reference/` needs updating at that point.

No manual test cases: the rollout-under-load e2e covers the drain guarantee, which meets the bar in `.claude/rules/manual-test-cases.md` for adequate automated coverage.

# Graceful Drain — Implementation Plan

Design: [../graceful-drain-design.md](../graceful-drain-design.md)
Issue: #1363

> Jira story references are placeholders pending stories under CONNLINK.

## Existing Code Analysis

What this builds on, all already merged:

- `cmd/mcp-broker-router/main.go` — SIGTERM registered (#1362). Shutdown sequence bounded (#1390): `serverDrainTimeout` 8s, `brokerDrainTimeout` 5s, `grpcDrainTimeout` 10s, `telemetryFlushTimeout` 4s, telemetry flushed last.
- `cmd/mcp-broker-router/broker.go:53-63` — `/healthz` returns a static 200; `/readyz` gates on `mcpBroker.IsReady()`.
- `internal/broker/broker.go` — `IsReady()` returns true when no servers are configured, or when any manager reports ready.
- `internal/controller/broker_router.go` — generates the Deployment. `replicas := int32(1)` at `:93`; readiness probe at `:235-248` with `PeriodSeconds: 10`, `FailureThreshold: 3`; Service exposes `http` and `grpc` off the same pod at `:285-300`. No `preStop`, no `terminationGracePeriodSeconds`.
- `internal/mcp-router/` — ext_proc server. No in-flight stream accounting today.
- `internal/routing/router_202511.go` — `initializeMCPServerSession` creates backend sessions lazily, guarded by a singleflight group.

What does not exist and must be added: any notion of lifecycle state, any accounting of in-flight work, and any pod-spec wiring for termination.

---

### Task 1: Lifecycle state and shared budgets (CONNLINK-TBD)

The budgets cannot live in `cmd/mcp-broker-router`: that is `package main` and Go cannot import it, so the controller in Task 6 would have to duplicate the values — reintroducing exactly the drift the design exists to prevent. They go in an importable package from the start, alongside the lifecycle state that Tasks 2, 3 and 4 all depend on.

**Files:**

- `internal/drain/state.go` (new)
- `internal/drain/state_test.go` (new)
- `internal/drain/budget.go` (new)
- `internal/drain/budget_test.go` (new)
- `cmd/mcp-broker-router/main.go`

**Acceptance criteria:**

- [ ] A `State` type with `serving`, `draining`, `terminating`, held as an atomic value and exported for the router and health handlers to read.
- [ ] Transitions are one-way and idempotent; a second SIGTERM does not reset state or restart the drain.
- [ ] Reads are allocation-free and safe from request-path goroutines.
- [ ] `internal/drain/budget.go` is the single home for `drainPropagationDelay`, `drainDeadline`, and the four teardown budgets merged in #1390, plus a `TotalGracePeriod()` derived from them.
- [ ] `cmd/mcp-broker-router` consumes the constants from `internal/drain`; none are redeclared there.
- [ ] A test asserts `TotalGracePeriod()` exceeds the sum of every individual budget.
- [ ] Unit tests cover each transition and concurrent read/write under `-race`.

**Verification:**

```bash
make lint
go test ./cmd/... -race -count=1
```

---

### Task 2: Readiness reflects drain (CONNLINK-TBD)

**Files:**

- `cmd/mcp-broker-router/broker.go`
- `cmd/mcp-broker-router/broker_test.go` (new)

**Acceptance criteria:**

- [ ] `/readyz` returns 503 in `draining` and `terminating`, regardless of `IsReady()`.
- [ ] `/readyz` behaviour in `serving` is unchanged.
- [ ] `/healthz` returns 200 in all three states; it reflects only whether the process can operate.
- [ ] Neither endpoint exposes internal state in its body.

**Verification:**

```bash
make lint
go test ./cmd/... -race -count=1
```

---

### Task 3: In-flight work accounting (CONNLINK-TBD)

Depends on Task 1 for the state type and the tracker's home.

**Files:**

- `internal/drain/tracker.go` (new)
- `internal/drain/tracker_test.go` (new)
- `internal/mcp-router/ext_proc_adapter.go`
- `internal/mcp-router/ext_proc_adapter_test.go`

**Acceptance criteria:**

- [ ] ext_proc streams are counted on open and released on close, including on error paths.
- [ ] New ext_proc streams are refused once draining.
- [ ] A `Wait(ctx)` returns when the count reaches zero or the context expires, whichever first.
- [ ] Counting adds no allocation on the per-request path, consistent with `docs/design/performance.md`.
- [ ] Unit tests with mock ext_proc streams cover open, close, error-path release, and refusal while draining.

**Verification:**

```bash
make lint
go test ./internal/mcp-router/... -race -count=1
go test ./internal/routing/... -bench=. -benchmem -run='^$'
```

---

### Task 4: Refuse new stateful sessions while draining (CONNLINK-TBD)

**Files:**

- `internal/routing/router_202511.go`
- `internal/routing/router_test.go`

Depends on Task 1 for the state type. The linearization point matters here: see the design's *Linearization point for session refusal*.

**Acceptance criteria:**

- [ ] The lifecycle check sits **inside** `initGroup.Do`, after the cache lookup and before `InitForClient`, so the atomic state read is the linearization point.
- [ ] An initialization that observes `serving` runs to completion and is counted as in-flight work; one that observes `draining` is refused before any upstream session exists.
- [ ] Callers that joined an already-running `Do` share its result rather than being refused, since that session was admitted while the pod was serving.
- [ ] Requests using an existing backend session mapping continue to route normally.
- [ ] The drain response is derived from process state only, never from a client-supplied header.
- [ ] Unit tests cover refusal, existing-session passthrough, singleflight joiners, and a race test flipping state concurrently with initialization under `-race`.

**Verification:**

```bash
make lint
go test ./internal/routing/... -race -count=1
```

---

### Task 5: Drain sequence in `run()` (CONNLINK-TBD)

**Files:**

- `cmd/mcp-broker-router/main.go`

Depends on Tasks 1, 3 and 4: the drain needs the state, something to wait on, and session gating.

**Acceptance criteria:**

- [ ] SIGTERM moves the process to `draining` before any shutdown begins.
- [ ] `brokerServer.Shutdown` is started at the beginning of the drain so HTTP requests drain concurrently with ext_proc streams under `drainDeadline`, rather than only in the later teardown.
- [ ] The drain waits for both to finish, or for `drainDeadline`, then moves to `terminating`.
- [ ] The bounded teardown from #1390 runs unchanged afterwards, including its `serverDrainTimeout` call as the backstop for anything still open.
- [ ] Budgets are consumed from `internal/drain`; none are redeclared here.
- [ ] Reaching the deadline is logged at warn with the outstanding HTTP and ext_proc counts.
- [ ] A test covers a delayed HTTP request completing inside the deadline, and one exceeding it.

**Verification:**

```bash
make lint
go test ./cmd/... -race -count=1
```

---

### Task 6: Pod lifecycle wiring (CONNLINK-TBD)

**Files:**

- `internal/controller/broker_router.go`
- `internal/controller/broker_router_test.go`
- `config/mcp-system/deployment-broker.yaml`
- `charts/mcp-gateway/templates/`

**Acceptance criteria:**

- [ ] The generated Deployment sets a `preStop` hook sleeping `drainPropagationDelay`, imported from `internal/drain`.
- [ ] `terminationGracePeriodSeconds` is `drain.TotalGracePeriod()`, never a literal.
- [ ] `drainPropagationDelay` is validated against a real cluster: measure the interval between pod deletion and the endpoint disappearing from the gateway's Envoy config, and record the observed value in the design. Raise the default if 5s does not cover it.
- [ ] The static manifest and the Helm chart match the controller output.
- [ ] A controller test asserts the grace period is greater than the sum of all budgets.
- [ ] `make generate-all` produces no diff.

**Verification:**

```bash
make lint
make generate-all && git diff --exit-code
make test-controller-integration
```

---

### Task 7: Drain metrics (CONNLINK-TBD)

**Files:**

- `internal/otel/metrics.go`
- `cmd/mcp-broker-router/lifecycle.go`

**Acceptance criteria:**

- [ ] Drain duration, requests completed during drain, and forced terminations are exported.
- [ ] Naming follows the existing `mcp_broker_*` conventions.
- [ ] Metrics emitted during drain are actually exported, which #1390's ordering change makes possible.
- [ ] Unit tests assert the instruments are registered and recorded.

**Verification:**

```bash
make lint
go test ./internal/otel/... -race -count=1
```

---

### Task 8: Rollout-under-load e2e (CONNLINK-TBD)

**Files:**

- `tests/e2e/graceful_drain_test.go` (new)
- `tests/e2e/test_cases.md`

**Acceptance criteria:**

- [ ] Sustained concurrent `tools/call` load across a `kubectl rollout restart` completes with no transport-level resets.
- [ ] Any failures observed are retryable protocol errors, not connection resets.
- [ ] The pod exits within `terminationGracePeriodSeconds` without being killed.
- [ ] Marked `Serial`, per `tests/e2e/CLAUDE.md`, since it restarts shared infrastructure.
- [ ] Cases added to `tests/e2e/test_cases.md` with the tags in `e2e_test_cases.md`.

**Verification:**

```bash
make test-e2e
```

---

### Task 9: Documentation (CONNLINK-TBD)

**Files:**

- `docs/guides/` — per [documentation.md](documentation.md)
- `docs/release-notes/`

**Acceptance criteria:**

- [ ] An operator-facing section covering what drain does, what it does not promise, and how to read the metrics.
- [ ] The `terminationGracePeriodSeconds` change documented, since it alters observable pod behaviour.
- [ ] Release notes entry per `.claude/rules/breaking-changes.md`.

**Verification:**

```bash
make lint
```

---

## Ordering

Task 1 gates everything: it introduces both the lifecycle state and the shared budget package that Tasks 2, 3, 4 and 6 all consume.

```text
Task 1 (state + budgets)
 ├─ Task 2 (readiness)        ─┐
 ├─ Task 3 (in-flight)        ─┤
 ├─ Task 4 (session refusal)  ─┤
 └─ Task 6 (pod spec)          │   ← needs budgets only, not 3/4
                               ▼
                        Task 5 (drain sequence)
                               │
                    ┌──────────┴──────────┐
                 Task 7 (metrics)   Task 8 (e2e)
```

Tasks 2, 3 and 4 are independent of each other and can land in parallel once Task 1 is in. Task 5 needs 3 and 4, since the drain needs something to wait on and something to gate. Task 6 needs only the budgets from Task 1, so it can land early. Tasks 7 and 8 come last: metrics need the states to exist, and the e2e needs the whole sequence wired.

Task 9 can be drafted alongside Task 6, once the observable pod behaviour is fixed.

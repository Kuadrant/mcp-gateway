# Graceful Drain — E2E Test Cases

Design: [../graceful-drain-design.md](../graceful-drain-design.md)

Format follows `tests/e2e/test_cases.md`. `Drain` is the feature tag; only the core rollout case is `Happy`, so that tag keeps its meaning.

### [Happy,Drain] Test tool calls survive a rollout restart under load

- When a client issues sustained concurrent tools/call requests whose individual durations are shorter than the drain deadline, and the mcp-gateway deployment is restarted with `kubectl rollout restart`, every request should either complete successfully or fail with a retryable JSON-RPC error. No request should fail with a transport-level reset or an ext_proc 5xx. The assertion is deliberately scoped to calls that fit inside the deadline; calls that outlast it are covered separately below, and the design does not promise them a clean outcome. The test runs Serial because it restarts shared infrastructure.

### [Drain] Test readiness reports draining while liveness stays healthy

- When a broker-router pod receives SIGTERM, its /readyz endpoint should begin returning 503 while /healthz continues to return 200. This asserts that drain state is *reported*, not that readiness is the mechanism withdrawing traffic — endpoint removal is driven by pod deletion, and the readiness probe's 10s period with a 3-failure threshold is far too slow to serve that purpose. The value here is observability, and correctness on paths where a pod drains without being deleted.

### [Drain] Test no new backend sessions are created while draining

- When a pod has entered the draining state and a client attempts to initialize a new stateful session against an upstream it has not used before, the gateway should refuse with a retryable error rather than creating a backend session it is about to abandon. Verified by asserting the upstream test server records no new session for that gateway session after drain begins.

### [Drain] Test existing sessions continue routing while draining

- When a pod is draining and a client issues a tools/call using a backend session mapping that already exists, the request should route and complete normally. Draining refuses new session creation only; it must not break sessions already established, since a replacement pod is expected to keep using them.

### [Drain] Test in-flight tool calls complete during drain

- When a slow tool call is in flight and the serving pod receives SIGTERM, the call should complete and return its result rather than being cut at the socket, provided it finishes within the drain deadline. Uses a test server tool with a controllable delay shorter than the deadline.

### [Drain] Test drain deadline is enforced and overdue calls fail bounded

- When a tool call outlasts the drain deadline, the pod should stop waiting, proceed to the bounded teardown, and exit within terminationGracePeriodSeconds rather than being killed by the kubelet. The overdue call is permitted to fail, and this is the bound the Happy case above deliberately excludes: assert the failure is bounded in time, and assert the forced termination via the drain log record on stdout rather than a Prometheus counter — the metrics endpoint is closed before the pod exits, so a drain-time counter can never be scraped. Uses a delay longer than the deadline.

### [Drain] Test HTTP requests drain alongside ext_proc streams

- When a slow HTTP request to the broker is in flight and the pod receives SIGTERM, the request should be given the drain deadline to complete rather than being cut when the teardown begins. Confirms brokerServer.Shutdown is started at the beginning of the drain rather than only in the later teardown, which is what makes the design's "HTTP and ext_proc work" guarantee true for both.

### [Drain] Test the pod exits within its grace period

- When the deployment is restarted, each terminating pod should exit before terminationGracePeriodSeconds elapses. A pod reaching the grace period would indicate the budget arithmetic has drifted from the pod spec, which is the failure the computed grace period exists to prevent.

### [Drain,Security] Test draining does not weaken request rejection

- When a pod is draining and a request arrives that would normally be rejected, it should still be rejected. Draining changes when traffic stops arriving, never what happens to traffic that does arrive; ext_proc failure_mode_allow remains false throughout.

### [Drain,Security] Test the drain response cannot be induced by a client header

- When a client sends headers attempting to signal drain state, the gateway should ignore them entirely. The drain response is derived from process state only, consistent with the rule that x-mcp-* routing metadata is router-set and never client-settable.

### [Drain] Test endpoint propagation stays within the configured delay

- When a broker-router pod is deleted, the interval between deletion and its endpoint disappearing from the gateway's Envoy configuration should remain below drainPropagationDelay. This is the design's one genuinely unbounded assumption — istiod convergence is not something the gateway controls — so it is worth a case that fails loudly if a cluster or Istio upgrade regresses it, rather than measuring it once during implementation and never again.

### [Drain] Test an idle SSE stream does not delay the drain

- When a client holds a GET /mcp SSE stream open with no request in flight and the pod is terminated, the drain should complete promptly rather than waiting out the full deadline. Guards the distinction between tracking in-flight requests and tracking ext_proc stream lifetimes; regressing to the latter would make every rollout pay the whole drain budget.

### [Drain] Test the drain response carries the documented contract

- When a draining pod refuses a new backend session, the response should be HTTP 503 with a Retry-After header and a JSON-RPC error body using the documented code, rather than a connection reset or an ext_proc 5xx. Also records what the official Go SDK does with that response, since the user documentation's retry guidance depends on whether the SDK reconnects transparently or surfaces the error.

### [Drain] Test steady-state behaviour is unaffected

- When no pod is terminating, session creation, tool invocation, and readiness reporting should behave exactly as before this feature. Regression safety for the normal path, since the drain checks sit on the request path.

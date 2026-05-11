# A2A E2E Scenarios

These scenarios describe the end-to-end behavior expected from A2A support in
MCP Gateway. They are intentionally written before gateway implementation so the
design for agent discovery, task routing, streaming, status, and policy has a
shared test target.

The scenarios assume a future A2A test fixture with deterministic agent cards
and task responses. The fixture should expose at least two agents so federation,
name conflicts, routing, and status behavior can be tested without external
network dependencies.

## [A2A] Federated agent discovery

- When two upstream A2A agents are registered with the gateway, the broker
  should fetch each upstream agent card and expose a federated discovery
  response through the gateway.
- The federated response should include both agents, their skills, supported
  capabilities, and a gateway-routable URL for each agent.
- Upstream URLs in agent cards should not cause clients to bypass the gateway.
  The gateway-served card or catalog should rewrite routable URLs to the
  gateway hostname and the selected A2A path shape.
- If two upstream agents use the same agent name or skill identifier, the
  response should either disambiguate them deterministically or report a clear
  conflict in status. The expected behavior should match the A2A discovery
  design.

## [A2A] Non-streaming task send

- When a client sends a non-streaming A2A task request through the gateway, the
  router should identify the target agent and forward the request to the correct
  upstream agent.
- The upstream agent should return a completed task with a deterministic text
  artifact.
- The response returned to the client should preserve the A2A task result shape
  and should not include MCP-specific rewrites.
- The gateway should expose protocol metadata for policy and observability, such
  as the A2A method, target agent, task ID, and streaming mode.

## [A2A] Streaming task send

- When a client sends a streaming A2A task request through the gateway, the
  request should route to the correct upstream agent without buffering the full
  response stream.
- The client should receive task status and artifact events in the order emitted
  by the upstream agent.
- The final event should indicate task completion.
- The gateway should preserve the streaming content type and should not apply
  MCP-specific SSE rewriting to A2A task events.

## [A2A] Task lookup

- When a client creates a task through the gateway and later sends a task lookup
  request for the same task ID, the gateway should route the lookup to the
  upstream agent that owns the task.
- Task ownership should be scoped so a different user or session cannot read a
  task unless the policy design explicitly allows it.
- If the gateway cannot find task ownership state, it should return a clear
  error instead of broadcasting the lookup to every registered agent.

## [A2A] Task cancellation

- When a client cancels a task through the gateway, the cancellation request
  should route to the upstream agent that owns the task.
- A successful cancellation should return an A2A task status with the cancelled
  state.
- A cancellation request from a different user or unauthorized session should be
  rejected according to the policy model.
- Repeated cancellation of a completed or already cancelled task should return
  deterministic behavior from the upstream agent or a gateway error defined by
  the task ownership design.

## [A2A] Agent card readiness and status

- When a registered A2A agent has a reachable and valid agent card, the gateway
  status endpoint should report the agent as ready, including last refresh time,
  skill count, supported capabilities, and the selected protocol binding.
- When an agent card is unreachable, malformed, or missing required fields, the
  gateway should mark that agent as not ready and include the validation or
  fetch error in status.
- Invalid agents should not appear in federated discovery unless the design
  chooses to expose degraded entries with warnings.
- Status checks should use structured gateway status data, not log parsing.

## [A2A] Policy-denied request

- When policy denies an A2A task request, the client should receive a denied
  response before the request reaches the upstream agent.
- The policy check should use gateway-produced metadata rather than trusting
  client-supplied A2A headers.
- A denied task request should be observable through status, logs, or metrics
  using the same metadata fields that policy receives.
- Agent discovery should remain consistent with policy behavior. A client should
  not be encouraged to call an agent or skill that policy will always deny,
  unless the discovery design explicitly chooses to expose denied capabilities.

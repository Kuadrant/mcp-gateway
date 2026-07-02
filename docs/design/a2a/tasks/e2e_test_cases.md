# A2A E2E Test Cases

> Test cases follow the format defined in `tests/e2e/test_cases.md`.
> Tags: `Happy` (PR gate), `A2A` (A2A feature suite), `A2ASecurity` (auth/security paths).

---

### [Happy,A2A] API Catalog lists registered agents and per-agent card returns correct skills

When an `A2AAgentRegistration` is created with a valid HTTPRoute pointing to an A2A test server,
the gateway's `GET /.well-known/api-catalog` endpoint should return an RFC 9727 catalog (RFC 9264
Linkset) containing a link to the agent's endpoint at `/a2a/{prefix}`. A subsequent
`GET /a2a/{prefix}/.well-known/agent-card.json` should return the upstream agent's Agent Card served
(cached) by the gateway, with the card's `url` field
rewritten to the gateway path (`/a2a/{prefix}`) rather than the upstream address, so an unmodified A2A
client following the card routes back through the gateway. The catalog entry should not appear until
the registration is Ready.

---

### [Happy,A2A] message/send routes to the correct upstream agent and returns a gateway task ID

When a client authenticated with an OAuth bearer sends a `message/send` request to a registered agent's
path (`/a2a/{prefix}`), the gateway should route the request to the correct upstream A2A agent,
return a response containing a gateway-generated task ID (not the upstream's task ID), and store
the task route mapping. The upstream agent should receive the request with the upstream task ID.

---

### [Happy,A2A] tasks/get resolves gateway task ID to upstream agent and returns task status

When a client sends a `tasks/get` request with a gateway task ID previously returned by
`message/send`, the gateway should resolve the task ID to the correct upstream agent, rewrite the
ID to the upstream task ID, forward the request, and return the upstream result to the client
with the gateway task ID restored in the response.

---

### [Happy,A2A] tasks/cancel propagates to upstream and returns canceled state

When a client sends a `tasks/cancel` request with a valid gateway task ID, the gateway should
route the request to the correct upstream agent with the upstream task ID, and the client should
receive a response reflecting the canceled task state.

---

### [Happy,A2A] SSE streaming delivers task updates with consistent gateway task IDs

When a client sends a `message/stream` request (the v0.3.0 streaming method, §7.2), the gateway should
deliver SSE chunks in real time. All `data:` events should contain the gateway task ID (not the
upstream task ID) across `result.id`, `result.taskId`, and `history[].taskId`. The stream should
complete when the upstream agent sends a terminal state (`completed`, `failed`, `canceled`, or
`rejected`) with `final: true`.

---

### [A2A] Agent deregistration removes agent from API catalog within one reconcile cycle

When an `A2AAgentRegistration` is deleted, the agent's link should no longer appear in
`GET /.well-known/api-catalog` within one reconcile cycle. A `message/send` request to the
deregistered agent's path should return JSON-RPC error `-32602` (unknown path prefix) after the
reconcile completes.

---

### [A2A] Multiple agents registered with distinct prefixes route independently

When two `A2AAgentRegistrations` are created with different `agentPrefix` values, the API Catalog
should list both agents at their respective paths (`/a2a/agent-a` and `/a2a/agent-b`). A
`message/send` request to `/a2a/agent-a` should route to agent A; a request to `/a2a/agent-b`
should route to agent B. There should be no cross-routing.

---

### [A2ASecurity] message/send without a valid bearer returns 401

When a client sends a `message/send` request to `/a2a/{prefix}` without an `Authorization: Bearer`
token, or with an expired or invalid one, the gateway's AuthPolicy should return 401 without
forwarding anything to the upstream agent. The upstream agent should receive no request.

---

### [A2ASecurity] message/send to unregistered path prefix returns JSON-RPC -32602

When an authenticated client sends a `message/send` request to `/a2a/{prefix}` where the
prefix does not match any registered `A2AAgentRegistration`, the gateway should return a JSON-RPC
error response with code `-32602` and not forward the request to any upstream agent.

---

### [A2ASecurity] x-a2a-agent header injected by client is stripped

When a client sends a request to `/a2a/{prefix}` with a manually-set `x-a2a-agent` header, the
gateway should strip this header before processing. The routing decision should be based solely on
the `:path` prefix, not on the injected header.

---

### [A2ASecurity] AuthPolicy denies a client lacking the agent role

When an AuthPolicy with per-agent RBAC is attached to the `/a2a` route and a client presents a valid
bearer whose `resource_access['a2a'].roles` does not include `agent:{prefix}`, Authorino should return
403 (using the router-set `x-a2a-agent` header) before the request reaches the upstream agent. A client
whose token includes the role is routed normally.

---

### [A2A] Agent Card skills are filtered by x-a2a-authorized (visibility, not enforcement)

When Authorino signs an `x-a2a-authorized` header with `allowed-capabilities.skills` listing a subset
of an agent's skills, `GET /a2a/{prefix}/.well-known/agent-card.json` returns a card containing only the
listed skills. With no `x-a2a-authorized` header (enforcement off), the full card is returned; a
tampered/unsigned header is rejected (fail closed). Note this controls card visibility only — it does
not block `message/send` to the agent.

---

### [A2A] MCP tools/list and tools/call are unaffected by A2A changes

When A2A support is fully deployed (agents registered, broker serving `/.well-known/api-catalog`,
router handling `/a2a/{prefix}`), a client performing MCP `tools/list` should receive the same federated
tool list as before. A `tools/call` request should route correctly to the MCP backend and return
the expected result. No regressions in MCP behavior.

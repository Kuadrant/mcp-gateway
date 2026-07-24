# A2A E2E Test Cases

> Test cases follow the format defined in `tests/e2e/test_cases.md`.
> Tags: `Happy` (PR gate), `A2A` (A2A feature suite), `A2ASecurity` (auth/security paths).

---

### [Happy,A2A] API Catalog lists registered agents and per-agent card returns correct skills

When an `A2AAgentRegistration` is created with a valid HTTPRoute pointing to an A2A test server,
the gateway's `GET /.well-known/api-catalog` endpoint should return an RFC 9727 catalog (RFC 9264
Linkset) containing a link to the agent's endpoint at `/a2a/{namespace}/{prefix}`. A subsequent
`GET /a2a/{namespace}/{prefix}/.well-known/agent-card.json` should return the upstream agent's Agent Card served
(cached) by the gateway **verbatim** — a signed card's JWS signature must survive byte-for-byte, so the
gateway does not rewrite it; the catalog link is what routes the client to the gateway path. The catalog
entry should not appear until the registration is Ready.

---

### [Happy,A2A] SendMessage routes to the correct upstream agent and passes the task ID through

When a client authenticated with an OAuth bearer sends a `SendMessage` request to a registered agent's
path (`/a2a/{namespace}/{prefix}`), the gateway should route the request to the correct upstream A2A agent
and return the response with the agent-assigned task ID **unchanged** at `result.task.id` (the v1.0
`SendMessageResponse` oneof; the gateway does not mint or rewrite it). The gateway should record
`(agent, taskID) → principal` so later calls can be ownership-checked; a `result.message` reply creates
no task and no record.

---

### [Happy,A2A] GetTask routes by path and returns task status with the ID unchanged

When a client sends a `GetTask` request carrying a task ID previously returned by `SendMessage`, the
gateway should route the request to the correct upstream agent **by path** (not by resolving the ID),
forward the body unchanged, and return the upstream result verbatim — the task ID is identical on the
way in and out.

---

### [Happy,A2A] CancelTask propagates to upstream and returns canceled state

When a client sends a `CancelTask` request with a valid task ID, the gateway should route the request
to the correct upstream agent by path with the task ID unchanged, and the client should receive a
response reflecting the canceled task state.

---

### [Happy,A2A] SSE streaming delivers task updates with the task ID passed through unchanged

When a client sends a `SendStreamingMessage` request (the v1.0 streaming method), the gateway should
deliver SSE chunks in real time, each `data:` event forwarded byte-for-byte with the agent-assigned task
ID intact at its identity field — `result.task.id` on the initial `task` event,
`result.statusUpdate.taskId`/`result.artifactUpdate.taskId` on updates. The gateway should create the
ownership record on the first event. The stream should complete when the upstream agent sends a terminal
state (`TASK_STATE_COMPLETED`, `TASK_STATE_FAILED`, `TASK_STATE_CANCELED`, or `TASK_STATE_REJECTED`),
and the ownership record should survive stream completion.

---

### [A2A] Agent deregistration removes agent from API catalog within one reconcile cycle

When an `A2AAgentRegistration` is deleted, the agent's link should no longer appear in
`GET /.well-known/api-catalog` within one reconcile cycle. A `SendMessage` request to the
deregistered agent's path should return JSON-RPC error `-32602` (unknown path prefix) after the
reconcile completes.

---

### [A2A] Multiple agents registered with distinct prefixes route independently

When two `A2AAgentRegistrations` are created with different `agentPrefix` values, the API Catalog
should list both agents at their respective paths (`/a2a/mcp-test/agent-a` and `/a2a/mcp-test/agent-b`). A
`SendMessage` request to `/a2a/mcp-test/agent-a` should route to agent A; a request to `/a2a/mcp-test/agent-b`
should route to agent B. There should be no cross-routing.

---

### [A2ASecurity] SendMessage without a valid bearer returns 401

When a client sends a `SendMessage` request to `/a2a/{namespace}/{prefix}` without an `Authorization: Bearer`
token, or with an expired or invalid one, the gateway's AuthPolicy should return 401 without
forwarding anything to the upstream agent. The upstream agent should receive no request.

---

### [A2ASecurity] SendMessage to unregistered path prefix returns JSON-RPC -32602

When an authenticated client sends a `SendMessage` request to `/a2a/{namespace}/{prefix}` where the
prefix does not match any registered `A2AAgentRegistration`, the gateway should return a JSON-RPC
error response with code `-32602` and not forward the request to any upstream agent.

---

### [A2ASecurity] x-a2a-agent header injected by client is stripped

When a client sends a request to `/a2a/{namespace}/{prefix}` with a manually-set `x-a2a-agent` header, the
gateway should strip this header before processing. The routing decision should be based solely on
the `:path` prefix, not on the injected header.

---

### [A2ASecurity] AuthPolicy denies a client lacking the agent role

When an AuthPolicy with per-agent RBAC is attached to the `/a2a` route and a client presents a valid
bearer whose `resource_access['a2a'].roles` does not include `agent:{namespace}/{prefix}`, Authorino
should return 403 (using the router-set, namespace-qualified `x-a2a-agent` header) before the request
reaches the upstream agent. A client whose token includes the role is routed normally.

---

### [A2ASecurity] A principal cannot GetTask or CancelTask another principal's task

When principal A creates a task via `SendMessage` and principal B (a different valid bearer `sub`) sends
a `GetTask` or `CancelTask` for that same task ID on the same agent, the gateway should fail closed with
`-32001 TaskNotFoundError` — indistinguishable from a nonexistent ID — before forwarding anything to
the upstream agent. Principal A performing the same call succeeds.

---

### [A2ASecurity] A continuation send naming another principal's task is rejected

When principal A creates a task and principal B sends a `SendMessage` or `SendStreamingMessage` whose
`message.taskId` (or `referenceTaskIds`) names A's task, the gateway should fail closed with `-32001`
before forwarding — a caller who knows a task ID cannot inject input into someone else's task. The
rejected send must also not overwrite the ownership record: principal A's subsequent `GetTask` succeeds.

---

### [A2ASecurity] Completed tasks remain ownership-protected

When principal A's task reaches a terminal state (`TASK_STATE_COMPLETED`), principal B's `GetTask` for
that task ID should still fail closed with `-32001`, while principal A's `GetTask` still returns the
completed task — the ownership record persists for the retention window rather than being deleted at
the terminal state.

---

### [A2A] Deferred v1.0 methods are rejected at the gateway, not forwarded

When an authenticated client sends a `ListTasks` (or `GetExtendedAgentCard` /
`pushNotificationConfig/*`) request to a registered agent's path, the gateway should return JSON-RPC
`-32004 UnsupportedOperationError` and the upstream agent should receive no request — a forwarded
`ListTasks` could return tasks across principals.

---

### [A2ASecurity] A continuation naming another principal's context is rejected

When principal A creates a task (establishing `contextId` C) and principal B sends a `SendMessage`
carrying `contextId` C — with or without a `taskId` — the gateway should fail closed with `-32001`
before forwarding, because B does not own context C. Principal A continuing the same context succeeds.
The ownership record for C is created insert-only and is not rebindable by B.

---

### [A2ASecurity] A card advertising a non-JSONRPC or off-gateway interface is not served

When an agent's card advertises an interface with a non-`JSONRPC` binding (`GRPC`/`HTTP+JSON`), a
non-`http(s)` scheme, a non-v1 `protocolVersion`, or a URL whose path/host is not the agent's gateway
path, the broker should fail validation: the card is not served (503), the agent is excluded from the
catalog, and the failure is logged. A card whose sole interface is JSONRPC at the gateway path serves
normally.

---

### [A2A] A registered agent enters the catalog only once its card is validated and cached

When an `A2AAgentRegistration` becomes Ready but its card has not yet been fetched, the agent should
**not** appear in `GET /.well-known/api-catalog`; once the broker fetches and validates the card, the
agent appears. An agent whose card later fails validation is removed from the catalog.

---

### [A2ASecurity] A non-v1 A2A-Version request is rejected before body parsing

When a client sends an A2A request declaring a non-v1 `A2A-Version`, the gateway should reject it with
`VersionNotSupportedError` rather than parsing ownership-sensitive fields under v1 assumptions.

---

### [A2A] MCP tools/list and tools/call are unaffected by A2A changes

When A2A support is fully deployed (agents registered, broker serving `/.well-known/api-catalog`,
router handling `/a2a/{namespace}/{prefix}`), a client performing MCP `tools/list` should receive the same federated
tool list as before. A `tools/call` request should route correctly to the MCP backend and return
the expected result. No regressions in MCP behavior.

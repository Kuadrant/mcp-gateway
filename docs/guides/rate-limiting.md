# Rate Limiting MCP Traffic with Kuadrant

This guide explains how to protect your Model Context Protocol (MCP) gateways and backend servers from abuse by configuring rate limits using [Kuadrant](https://kuadrant.io/).

## 1. Introduction & Context

Rate limiting is critical for safeguarding MCP traffic. Large language models (LLMs) and other client applications can easily generate "prompt storms" or fall into infinite loops of recursive tool calls. Without proper limits, this traffic can lead to Denial of Service (DoS), backend resource exhaustion, and high operational costs.

According to the **NSA MCP Security Guidance**, operators must place protective boundaries around MCP servers to constrain how many requests a client or a specific capability can make over time. Kuadrant's `RateLimitPolicy` (RLP) integrates seamlessly with the Gateway API, providing a powerful way to enforce these constraints at the ingress layer before requests ever reach your MCP backends.

Below are three common scenarios for applying rate limits to your MCP Gateway.

## Prerequisites

- A running MCP Gateway deployment (see [Installation Guide](./how-to-install-and-configure.md))
- [Kuadrant operator](https://docs.kuadrant.io/) installed on the cluster with Limitador configured
- At least one `MCPServerRegistration` and its backing `HTTPRoute` configured (see [Register MCP Servers](./register-mcp-servers.md))
- `kubectl` configured and pointing at the target cluster

---

## 2. Scenario A: Whole Gateway Scope

A gateway-scoped rate limit applies to all traffic traversing the Gateway. This is useful for defining global ceilings that protect your entire infrastructure from broad volumetric attacks.

In this scenario, the `RateLimitPolicy` targets the `Gateway` resource directly. When you define limits at the top-level `spec.limits`, those limits act as **defaults** — any route-level `RateLimitPolicy` attached to an `HTTPRoute` under this Gateway will replace them for that specific route. If you need to enforce a strict ceiling that cannot be overridden by route-level policies, use `spec.overrides.limits` instead.

### Step 1: Apply the policy

The following example restricts all traffic through the `mcp-gateway` to a maximum of 1000 requests per minute.

```bash
kubectl apply -f - <<EOF
apiVersion: kuadrant.io/v1
kind: RateLimitPolicy
metadata:
  name: global-mcp-rate-limit
  namespace: gateway-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
  limits:
    "global-limit":
      rates:
        - limit: 1000
          window: 1m
EOF
```

> **Note:** All requests matching any route bound to `mcp-gateway` will increment this counter. If the limit is exceeded, Kuadrant will return an HTTP `429 Too Many Requests` response.
>
> **Tip:** To enforce a hard limit that overrides any route-level policies, replace `spec.limits` with `spec.overrides.limits` in the YAML above.

### Step 2: Verify the policy

Confirm the policy is accepted and enforced:

```bash
kubectl get ratelimitpolicy global-mcp-rate-limit -n gateway-system \
  -o jsonpath='{.status.conditions[?(@.type=="Enforced")].status}'
# expected: True
```

Once enforced, send more than 1000 requests within one minute to any route on the gateway. Requests that exceed the limit will receive an HTTP `429 Too Many Requests` response.

---

## 3. Scenario B: Per-Backend Scope

Sometimes you want to limit traffic directed to a specific MCP server because it connects to an expensive or fragile underlying resource (like a legacy database).

In this scenario, we target an `HTTPRoute` rather than the whole Gateway. This isolates the rate limits to that specific backend server without affecting the rest of the MCP tools and servers on the Gateway. A route-level policy will replace any gateway-level default limits for that route.

### Step 1: Apply the policy

The following example targets the `my-mcp-server-route` (which exposes a specific MCP server) and restricts it to 50 requests per second.

```bash
kubectl apply -f - <<EOF
apiVersion: kuadrant.io/v1
kind: RateLimitPolicy
metadata:
  name: weather-backend-rate-limit
  namespace: mcp-test
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: my-mcp-server-route
  limits:
    "weather-limit":
      rates:
        - limit: 50
          window: 1s
EOF
```

### Step 2: Verify the policy

Confirm the policy is accepted and enforced:

```bash
kubectl get ratelimitpolicy weather-backend-rate-limit -n mcp-test \
  -o jsonpath='{.status.conditions[?(@.type=="Enforced")].status}'
# expected: True
```

Once enforced, requests exceeding 50 per second to the `my-mcp-server-route` will receive an HTTP `429 Too Many Requests` response, while other routes remain unaffected.

---

## 4. Scenario C: Per-Tool Scope

Granular, tool-specific rate limiting is the most effective way to prevent abuse of highly sensitive or costly capabilities. The MCP Gateway automatically injects the `x-mcp-toolname` HTTP header into requests directed to specific tools. We can use this header as a counter key in our `RateLimitPolicy` to enforce distinct limits per tool.

In this scenario, we define a limit that groups requests based on the value of the `x-mcp-toolname` header. If an LLM recursively calls a tool like `execute_sql`, only the limit for `execute_sql` will be exhausted; other tools will remain available.

### Step 1: Apply the policy

The following example targets the `my-mcp-server-route` but applies a limit of 10 requests per minute *per tool*.

```bash
kubectl apply -f - <<EOF
apiVersion: kuadrant.io/v1
kind: RateLimitPolicy
metadata:
  name: tool-specific-rate-limit
  namespace: mcp-test
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: my-mcp-server-route
  limits:
    "per-tool-limit":
      rates:
        - limit: 10
          window: 1m
      counters:
        - expression: "request.?headers[?'x-mcp-toolname'].orValue('__no_tool__')"
EOF
```

The counter expression uses CEL's optional syntax (`?.` and `orValue()`) to safely handle cases where the `x-mcp-toolname` header is not present. The MCP Gateway only sets this header for `tools/call` requests — it is absent for other MCP methods such as `initialize`, `tools/list`, or `resources/read`. When the header is missing, the expression evaluates to the fallback value `__no_tool__`, which groups all non-tool requests into a single shared counter.

In this setup:
- A `tools/call` request with `x-mcp-toolname: query_users` increments the `query_users` counter.
- A `tools/call` request with `x-mcp-toolname: execute_sql` increments a separate `execute_sql` counter.
- An `initialize` or `tools/list` request (where the header is absent) increments the shared `__no_tool__` counter.

### Step 2: Verify the policy

Confirm the policy is accepted and enforced:

```bash
kubectl get ratelimitpolicy tool-specific-rate-limit -n mcp-test \
  -o jsonpath='{.status.conditions[?(@.type=="Enforced")].status}'
# expected: True
```

Once enforced, calling a specific tool more than 10 times per minute will return an HTTP `429 Too Many Requests` response for that tool only, while other tools on the same route remain available.

---

## Next Steps

By combining these scopes, you can create a robust, multi-layered defense strategy that aligns with NSA security recommendations for MCP deployments.

- [Authorization](./authorization.md) — Configure fine-grained, per-tool access control using AuthPolicy
- [Authentication](./authentication.md) — Set up identity verification for MCP clients
- [Auditing](./auditing.md) — Enable access logging for MCP traffic
- [Observability](./observability.md) — Monitor MCP request metrics and traces
- [Tool Revocation](./tool-revocation.md) — Dynamically revoke access to individual tools

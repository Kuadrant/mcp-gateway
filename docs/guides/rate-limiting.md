# Rate Limiting MCP Traffic with Kuadrant

This guide covers configuring rate limiting for MCP Gateway using Kuadrant RateLimitPolicy.

- **Whole gateway** — a global ceiling across all routes
- **Per backend** — limits scoped to a single MCP server's HTTPRoute
- **Per tool** — granular counters keyed on the `x-mcp-toolname` header

## Why rate limit MCP traffic

Rate limiting is critical for safeguarding MCP traffic. Large language models (LLMs) and other client applications can easily generate "prompt storms" or fall into infinite loops of recursive tool calls. Without proper limits, this traffic can lead to Denial of Service (DoS), backend resource exhaustion, and high operational costs.

According to the **NSA MCP Security Guidance**, operators must place protective boundaries around MCP servers to constrain how many requests a client or a specific capability can make over time. Kuadrant's `RateLimitPolicy` (RLP) integrates seamlessly with the Gateway API, providing a powerful way to enforce these constraints at the ingress layer before requests ever reach your MCP backends.

## Prerequisites

- A running MCP Gateway deployment (see [Installation Guide](./how-to-install-and-configure.md))
- [Kuadrant operator](https://docs.kuadrant.io/) installed on the cluster with Limitador configured
- At least one `MCPServerRegistration` and its backing `HTTPRoute` configured (see [Register MCP Servers](./register-mcp-servers.md))
- `kubectl` configured and pointing at the target cluster

> **Note:** The YAML manifests below use placeholder values (`<gateway-namespace>`, `<gateway-name>`, `<route-namespace>`, `<route-name>`). Replace these with your actual cluster resource names before applying.

---

## Scenario A: Whole Gateway Scope

A gateway-scoped rate limit applies to all traffic traversing the Gateway. This is useful for defining global ceilings that protect your entire infrastructure from broad volumetric attacks.

In this scenario, the `RateLimitPolicy` targets the `Gateway` resource directly. When you define limits at the top-level `spec.limits`, those limits act as **defaults** — they apply only to routes that do not have a more-specific `RateLimitPolicy` attached to their `HTTPRoute`. Any route-level policy replaces the gateway defaults for that specific route. If you need to enforce a strict ceiling that cannot be overridden by route-level policies, use `spec.overrides.limits` instead.

### Step 1: Apply the policy

The following example restricts all traffic through the gateway to a maximum of 1000 requests per minute.

```bash
kubectl apply -f - <<EOF
apiVersion: kuadrant.io/v1
kind: RateLimitPolicy
metadata:
  name: global-mcp-rate-limit
  namespace: <gateway-namespace>
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: <gateway-name>
  limits:
    "global-limit":
      rates:
        - limit: 1000
          window: 1m
EOF
```

> **Note:** All requests matching any route bound to the gateway will increment this counter. Because `spec.limits` defines a **default**, these limits apply only to routes without their own `RateLimitPolicy`. If the limit is exceeded, Kuadrant will return an HTTP `429 Too Many Requests` response.
>
> **Tip:** To enforce a hard limit that overrides any route-level policies, replace `spec.limits` with `spec.overrides.limits` in the YAML above.

### Step 2: Verify the policy

Confirm the policy is accepted and enforced:

```bash
kubectl get ratelimitpolicy global-mcp-rate-limit -n <gateway-namespace> \
  -o jsonpath='{.status.conditions[?(@.type=="Enforced")].status}'
# expected: True
```

Once enforced, send more than 1000 requests within one minute to any route on the gateway. Requests that exceed the limit will receive an HTTP `429 Too Many Requests` response.

---

## Scenario B: Per-Backend Scope

Sometimes you want to limit traffic directed to a specific MCP server because it connects to an expensive or fragile underlying resource (like a legacy database).

In this scenario, we target an `HTTPRoute` rather than the whole Gateway. This isolates the rate limits to that specific backend server without affecting the rest of the MCP tools and servers on the Gateway. A route-level policy replaces any gateway-level default limits for that route.

### Step 1: Apply the policy

The following example targets a specific MCP server's route and restricts it to 50 requests per second.

```bash
kubectl apply -f - <<EOF
apiVersion: kuadrant.io/v1
kind: RateLimitPolicy
metadata:
  name: backend-rate-limit
  namespace: <route-namespace>
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: <route-name>
  defaults:
    strategy: merge
    limits:
      "backend-limit":
        rates:
          - limit: 50
            window: 1s
EOF
```

### Step 2: Verify the policy

Confirm the policy is accepted and enforced:

```bash
kubectl get ratelimitpolicy backend-rate-limit -n <route-namespace> \
  -o jsonpath='{.status.conditions[?(@.type=="Enforced")].status}'
# expected: True
```

Once enforced, requests exceeding 50 per second to the targeted route will receive an HTTP `429 Too Many Requests` response, while other routes remain unaffected.

---

## Scenario C: Per-Tool Scope (Coming Soon)

Granular rate limiting based on the `x-mcp-toolname` header is currently disabled pending an upstream architectural update to the Kuadrant Envoy router's header-mutation phase. This section will be updated with configuration examples once the header can be securely evaluated.

---

## Next Steps

By combining these scopes, you can create a robust, multi-layered defense strategy that aligns with NSA security recommendations for MCP deployments.

- [Authorization](./authorization.md) — Configure fine-grained, per-tool access control using AuthPolicy
- [Authentication](./authentication.md) — Set up identity verification for MCP clients
- [Auditing](./auditing.md) — Enable access logging for MCP traffic
- [Observability](./observability.md) — Monitor MCP request metrics and traces
- [Tool Revocation](./tool-revocation.md) — Dynamically revoke access to individual tools

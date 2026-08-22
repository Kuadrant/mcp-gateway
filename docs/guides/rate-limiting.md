# Rate Limiting MCP Traffic with Kuadrant

This guide explains how to protect your Model Context Protocol (MCP) gateways and backend servers from abuse by configuring rate limits using [Kuadrant](https://kuadrant.io/).

## 1. Introduction & Context

Rate limiting is critical for safeguarding MCP traffic. Large language models (LLMs) and other client applications can easily generate "prompt storms" or fall into infinite loops of recursive tool calls. Without proper limits, this traffic can lead to Denial of Service (DoS), backend resource exhaustion, and high operational costs.

According to the **NSA MCP Security Guidance**, operators must place protective boundaries around MCP servers to constrain how many requests a client or a specific capability can make over time. Kuadrant's `RateLimitPolicy` (RLP) integrates seamlessly with the Gateway API, providing a powerful way to enforce these constraints at the ingress layer before requests ever reach your MCP backends.

Below are three common scenarios for applying rate limits to your MCP Gateway.

---

## 2. Scenario A: Whole Gateway Scope

A gateway-scoped rate limit applies to all traffic traversing the Gateway. This is useful for defining global ceilings that protect your entire infrastructure from broad volumetric attacks.

In this scenario, the `RateLimitPolicy` targets the `Gateway` resource directly. The limit defined here applies across all routes and backends exposed by this gateway.

### Example: Global Gateway Rate Limit

The following example restricts all traffic through the `mcp-gateway` to a maximum of 1000 requests per minute.

```yaml
apiVersion: kuadrant.io/v1beta2
kind: RateLimitPolicy
metadata:
  name: global-mcp-rate-limit
  namespace: mcp-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: Gateway
    name: mcp-gateway
  limits:
    "global-limit":
      rates:
        - limit: 1000
          duration: 1m
```

*Note: All requests matching any route bound to `mcp-gateway` will increment this counter. If the limit is exceeded, Kuadrant will return an HTTP 429 Too Many Requests.*

---

## 3. Scenario B: Per-Backend Scope

Sometimes you want to limit traffic directed to a specific MCP server because it connects to an expensive or fragile underlying resource (like a legacy database). 

In this scenario, we target an `HTTPRoute` rather than the whole Gateway. This isolates the rate limits to that specific backend server without affecting the rest of the MCP tools and servers on the Gateway.

### Example: Per-Route Backend Rate Limit

The following example targets the `weather-mcp-route` (which exposes a specific weather MCP server) and restricts it to 50 requests per second.

```yaml
apiVersion: kuadrant.io/v1beta2
kind: RateLimitPolicy
metadata:
  name: weather-backend-rate-limit
  namespace: mcp-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: weather-mcp-route
  limits:
    "weather-limit":
      rates:
        - limit: 50
          duration: 1s
```

---

## 4. Scenario C: Per-Tool Scope

Granular, tool-specific rate limiting is the most effective way to prevent abuse of highly sensitive or costly capabilities. The MCP Gateway automatically injects the `x-mcp-toolname` HTTP header into requests directed to specific tools. We can use this header as a counter key in our `RateLimitPolicy` to enforce distinct limits per tool.

In this scenario, we define a limit that groups requests based on the value of the `x-mcp-toolname` header. If an LLM recursively calls a tool like `execute_sql`, only the limit for `execute_sql` will be exhausted; other tools will remain available.

### Example: Per-Tool Rate Limit

The following example targets the `database-mcp-route` but applies a limit of 10 requests per minute *per tool*. 

```yaml
apiVersion: kuadrant.io/v1beta2
kind: RateLimitPolicy
metadata:
  name: tool-specific-rate-limit
  namespace: mcp-system
spec:
  targetRef:
    group: gateway.networking.k8s.io
    kind: HTTPRoute
    name: database-mcp-route
  limits:
    "per-tool-limit":
      rates:
        - limit: 10
          duration: 1m
      counters:
        - "request.headers.x-mcp-toolname"
```

In this setup:
- A request with `x-mcp-toolname: query_users` increments the `query_users` counter.
- A request with `x-mcp-toolname: execute_sql` increments a separate `execute_sql` counter.
- If the header is missing, Kuadrant handles it based on its default counter behavior, but the gateway ensures this header is present for tool execution requests.

By combining these scopes, you can create a robust, multi-layered defense strategy that aligns with NSA security recommendations for MCP deployments.

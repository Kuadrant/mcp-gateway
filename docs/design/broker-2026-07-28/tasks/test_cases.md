# Broker 2026-07-28 Test Cases

## Integration Tests

Tests that verify broker internals with mock upstreams. No Envoy, no real gateway — direct broker API calls with controlled upstream responses.

### AggregateCache returns min of non-zero ttlMs

Given two upstreams with `ttlMs:60000` and `ttlMs:30000`, both `cacheScope:"public"`, `AggregateCache` returns `(30000, "public")`.

### AggregateCache returns private when any upstream is private

Given two upstreams — one `cacheScope:"public"`, one `cacheScope:"private"` — `AggregateCache` returns `cacheScope:"private"`. The ttlMs is the min of non-zero values.

### AggregateCache returns zero ttlMs when all upstreams are zero

Given two upstreams both with `ttlMs:0` and `cacheScope:"public"`, `AggregateCache` returns `(0, "public")`. `ttlMs:0` triggers `ShouldFetchFresh` but does not force private scope.

### AggregateCache ignores ttlMs zero for scope aggregation

Given upstreams with `ttlMs:60000, cacheScope:"public"` and `ttlMs:0, cacheScope:"public"`, `AggregateCache` returns `(60000, "public")`. The `ttlMs:0` server is always-fetched by the broker but does not force private scope — only `cacheScope:"private"` and `userSpecificList` drive scope.

### AggregateCache single upstream passthrough

Given one upstream with `ttlMs:45000, cacheScope:"public"`, `AggregateCache` returns `(45000, "public")` unchanged.

### AggregateCache empty input

Given no contributing upstreams, `AggregateCache` returns `(0, "public")`. The SDK serializes `cacheScope` without `omitempty`, so an empty string fails spec validation.

### ShouldFetchFresh triggers on cacheScope private

`ProtocolHandler2026.ShouldFetchFresh` returns `true` when upstream metadata has `cacheScope:"private"`, regardless of `ttlMs` value or CRD `userSpecificList` field.

### ShouldFetchFresh triggers on ttlMs zero

`ProtocolHandler2026.ShouldFetchFresh` returns `true` when upstream metadata has `ttlMs:0`, regardless of `cacheScope` value.

### ShouldFetchFresh false for public non-zero

`ProtocolHandler2026.ShouldFetchFresh` returns `false` when upstream metadata has `cacheScope:"public"` and `ttlMs > 0`.

### ShouldFetchFresh 2025 uses CRD field only

`ProtocolHandler2025.ShouldFetchFresh` returns the value of `userSpecificList` from the CRD, ignoring cache metadata entirely.

### cacheScope private supersedes userSpecificList false

When a 2026 upstream has `userSpecificList:false` on the CRD but reports `cacheScope:"private"`, the broker includes it in the `perRequestServers` list. Verified by checking `perRequestServers` after `OnConfigChange`.

### perRequestServers rebuilt immediately on upstream metadata change

When an upstream's `cacheScope` changes from `"public"` to `"private"` (detected on re-list), the broker atomically rebuilds `perRequestServers` immediately. The server appears in the fresh-fetch list on the next client request without waiting for `OnConfigChange`.

### cacheMetadata defaults for 2025 upstreams

A 2025 upstream that returns no `ttlMs`/`cacheScope` in its list response gets defaults: `TTLMs:0`, `CacheScope:"public"`.

### filteringMiddleware sets ttlMs and cacheScope on 2026 tools/list

When the middleware processes a `tools/list` result for a 2026 client, it calls `AggregateCache` **before** `FilterTools` (which strips `kuadrant/id` from tool Meta) and sets the result's `TTLMs` and `CacheScope` fields.

### filteringMiddleware sets ttlMs and cacheScope on 2026 prompts/list

Same as tools/list but for `prompts/list` — aggregated cache fields set on the prompt list result.

### promptsForProtocol filters prompts by upstream version

When a 2026 client sends `prompts/list`, the middleware calls `promptsForProtocol` which returns only prompts from 2026-capable upstreams. Prompts from 2025-only upstreams are excluded. A 2025 client sees only prompts from 2025-capable upstreams. Mirrors the existing `toolsForProtocol` behavior.

### promptsForProtocol cache rebuilt on prompt changes

When prompts are added or removed from the gateway server, the `statefulPrompts`/`statelessPrompts` caches are rebuilt alongside the tool caches. Verified by checking that a newly registered prompt from a 2026 upstream appears in the 2026 prompt set.

### compatHandler strips ttlMs and cacheScope for 2025 clients

When a 2025 client receives a `tools/list` response that has `ttlMs` and `cacheScope` set by the middleware, the compat handler's `rewriteToolsList` removes them. Existing test coverage may already verify this — confirm and extend if needed.

## E2E Tests

Tests that require the full gateway stack: client → Envoy → router → broker → upstream MCP servers.

---
test_suite: broker_2026_test.go
tags: Happy,Broker2026,Security
---

### [Happy,Broker2026] 2026 client tools/list returns ttlMs and cacheScope

A 2026 client sends `tools/list` to the gateway. The upstream 2026 test server reports `ttlMs:60000` and `cacheScope:"public"`. The response includes these fields at the top level of the list result alongside the tools.

### [Happy,Broker2026] cacheScope private triggers per-user tool fetch

A 2026 upstream reports `cacheScope:"private"`. A 2026 client sends `tools/list` with auth headers. The broker fetches per-user tools from that upstream using the client's credentials, without `userSpecificList:true` on the MCPServerRegistration. The response includes the user-specific tools and `cacheScope:"private"`.

### [Happy,Broker2026] 2025 client response unchanged with 2026 upstreams

A 2025 client sends `tools/list` to a gateway with 2026 upstreams reporting `ttlMs` and `cacheScope`. The response does not include these fields. Tool list content matches pre-2026 behavior.

### [Happy,Broker2026] Mixed protocol gateway serves both client versions

A gateway has both 2025 and 2026 upstreams. A 2026 client `tools/list` returns 2026-upstream tools with aggregated `ttlMs`/`cacheScope`. A 2025 client `tools/list` returns 2025-upstream tools without cache fields. Neither client sees tools from unsupported protocol upstreams.

### [Broker2026] 2026 upstream notification triggers tool refresh

A 2026 upstream sends `tools/list_changed`. The broker receives it via `subscriptions/listen`, re-lists tools, and notifies subscribed 2026 clients. Verifies the notification mechanism works end-to-end through the real gateway.

### [Broker2026] 2025 upstream GET SSE notification unaffected

A 2025 upstream sends `tools/list_changed` via GET SSE. The broker processes it and refreshes tools normally. Verifies 2025 notification path is not regressed. Existing notification e2e tests may already cover this — extend if needed rather than duplicating.

### [Happy,Broker2026] 2026 client prompts/list excludes 2025-only prompts

A gateway has both 2025 and 2026 upstreams, each with prompts. A 2026 client sends `prompts/list`. The response includes only prompts from the 2026 upstream. Prompts from the 2025-only upstream are absent. A 2025 client sends `prompts/list` and sees only the 2025 upstream's prompts.

### [Broker2026,Security] Private scope prevents cross-user tool list leak

Two clients (different auth headers) send `tools/list` to a gateway with one `cacheScope:"private"` upstream and one `cacheScope:"public"` upstream. Both see the same public tools. The private upstream's tools reflect each client's credentials. The aggregated response has `cacheScope:"private"`.

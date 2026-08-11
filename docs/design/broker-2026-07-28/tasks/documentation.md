# Broker 2026-07-28 Documentation Plan

Documentation for broker 2026-07-28 protocol support, organized by user goals.

## No New User-Facing Guide Required

The broker's 2026 protocol support is transparent to gateway operators. There are no new CRD fields, CLI flags, or configuration options. The broker automatically detects upstream protocol versions and applies the correct behavior:

- `ttlMs` and `cacheScope` are derived from upstream responses, not configured
- `cacheScope:"private"` from 2026 upstreams triggers per-user fetching automatically
- `subscriptions/listen` replaces GET SSE for 2026 upstreams without operator action
- `userSpecificList` continues to work for 2025 upstreams unchanged

The existing `docs/guides/scaling.md` and `docs/guides/authentication.md` guides remain accurate.

## Security Architecture Update (`docs/design/security-architecture.md`) ✅

Updated in task 5. Added `cacheScope` correctness (pessimistic aggregation), `filterUserHeaders` credential stripping, and broker→upstream per-user fetch boundary to the data crossing table.

## Design Doc Update (`docs/design/overview.md`) ✅

Updated in task 5. Added dual-protocol support bullet to broker responsibilities with pointer to the broker-2026-07-28 design doc.

## ttlMs Freshness Semantics

Covered in the design doc's constraints section and `AggregateCache` implementation. No separate doc needed — the rules are:
- Aggregated `ttlMs` is `min(non-zero)` across upstreams — bounds freshness to the most volatile upstream
- `ttlMs:0` upstreams are always-fetched and don't contribute to the aggregate
- The aggregate is informational for clients; the broker's own freshness is backstopped by the manager's periodic re-list

## API Reference — No Changes

`userSpecificList` stays for 2025 backward compat. No new CRD fields. No updates to `docs/reference/` required.

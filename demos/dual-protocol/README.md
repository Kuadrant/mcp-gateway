# Dual Protocol Demo

Demonstrates a single MCP Gateway serving both 2025-11-25 (stateful) and 2026-07-28 (stateless) clients.

## Prerequisites

Run `make local-env-setup` — this deploys both the everything server (2025) and the stateless server (2026) on the same gateway.

## Run

```bash
go run ./demos/dual-protocol/
```

Or with a custom gateway URL:

```bash
GATEWAY_URL=https://mcp.example.com/mcp go run ./demos/dual-protocol/
```

## What it shows

1. **2026 client via `/mcp`** — SDK negotiates 2026-07-28, sees only stateless server tools
2. **2025 client via `/mcp`** — SDK falls back to initialize, sees only stateful server tools plus `discover_tools`/`select_tools`
3. **`/mcp/stateful` route** — forces 2025-11-25 regardless of client capability
4. **`/mcp/stateless` route** — forces 2026-07-28 regardless of client capability

Each step lists the visible tools and calls one to prove routing works end-to-end.

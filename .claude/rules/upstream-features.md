# Upstream Feature Changes

Read `docs/design/backend-mcp-management.md` before modifying broker or upstream logic.

When adding new upstream/broker features:

- Update `MCPManager` in `internal/broker/upstream/manager.go`
- Add corresponding broker handling in `internal/broker/broker.go`
- Update config types in `internal/config/types.go` if new config fields needed
- Add unit tests alongside the implementation
- Add e2e test in `tests/e2e/`

## MCP Protocol Version Negotiation

### Problem

The MCP protocol is versioned (e.g. `2024-11-05`, `2025-03-26`, `2025-06-18`,
`2025-11-25`) and new versions are released over time. The gateway connects to
arbitrary upstream MCP servers, which may speak different versions. The gateway must:

1. Negotiate a protocol version with each upstream that both sides support
2. Reject upstreams that only speak versions the gateway does not understand
3. Keep working when the MCP ecosystem moves to a new version, without code changes
   for every release

The set of versions the gateway understands is owned by the
`github.com/mark3labs/mcp-go` library, not by this repo.

### Solution

The broker delegates negotiation to the mark3labs client. During `MCPServer.Connect`
(`internal/broker/upstream/mcp.go`) the broker sends an `initialize` request proposing
`mcp.LATEST_PROTOCOL_VERSION` — the newest version the linked library knows. The
upstream replies with the version it chose:

- If the reply is in `mcp.ValidProtocolVersions`, the client accepts it and the
  negotiated version is stored on the upstream (`MCPServer.ProtocolInfo()`).
- Otherwise the client returns an `UnsupportedProtocolVersionError` and `Connect`
  fails; the broker marks the server not ready and the MCPServerRegistration status
  reports `unsupported protocol version`.

A stock mark3labs server echoes the client's proposed version when it is valid, so a
normal upstream negotiates the latest. An upstream can still pin an older-but-valid
version (e.g. behind a proxy or an older SDK); the broker accepts it.

The negotiated version is surfaced on the broker `/status` endpoint per server as
`protocolValidation.supportedVersion` (populated in `MCPManager.setStatus`), with
`expectedVersion` set to the version the broker proposed.

> **Note:** the gateway does not pin a version itself. The floor and ceiling of
> supported versions are whatever `mcp.ValidProtocolVersions` contains in the linked
> `mark3labs/mcp-go` release.

### Testing strategy

Two layers, all automated:

- **Unit — version matrix** (`internal/broker/upstream/protocol_version_test.go`):
  iterates `mcp.ValidProtocolVersions` against a fake upstream pinned to each version,
  asserting the broker accepts it and records the negotiated version. The matrix is
  driven by the library's own list, so it expands automatically when the library
  learns a new version. A negative case asserts an out-of-range version
  (`2021-11-05`) is rejected.
- **Unit — status** (`internal/broker/upstream/manager_test.go`): asserts
  `setStatus` populates `protocolValidation` from the negotiated version.

### Reacting to a new protocol version

When the MCP spec ships a new version:

1. Bump `github.com/mark3labs/mcp-go` in `go.mod`. `LATEST_PROTOCOL_VERSION` and
   `ValidProtocolVersions` come from the library.
2. Run `make test-unit`. The version-matrix unit test automatically picks up the new
   entry in `ValidProtocolVersions` and exercises it — no test edits required.
3. If the new version adds or removes capabilities the broker depends on, update the
   broker handling and add coverage. See `docs/design/backend-mcp-management.md`.

# Tool Schema Validation Design

**Issue**: [#662](https://github.com/Kuadrant/mcp-gateway/issues/662)
**Date**: 2026-03-25

## Problem

When an upstream MCP server presents a tool with an invalid schema (e.g., `"type": "int"` instead of `"integer"`), the gateway serves it to all clients. Clients like Claude validate tool schemas against JSON Schema draft 2020-12 and reject ALL tools when any single tool is invalid, making the entire gateway unusable.

## Solution

Validate tool schemas against the MCP Tool specification before registering them with the gateway. Provide a configurable policy (`InvalidToolPolicy`) that controls behavior when invalid tools are detected.

## Policy

A new CLI flag `--invalid-tool-policy` controls behavior. Two values:

- **`FilterOut`** (default): Skip invalid tools, serve the remaining valid tools. Mark the server status with details about filtered tools. If ALL tools from a server are invalid, mark the server as not ready.
- **`RejectServer`**: If any tool has an invalid schema, reject ALL tools from that server and mark it as not ready.

The policy type and constants live in `internal/broker/upstream/validate.go`:

```go
type InvalidToolPolicy string

const (
    InvalidToolPolicyFilterOut    InvalidToolPolicy = "FilterOut"
    InvalidToolPolicyRejectServer InvalidToolPolicy = "RejectServer"
)
```

## Validation Rules

Validate each `mcp.Tool` against the MCP Tool schema:

```typescript
interface Tool {
  name: string;
  inputSchema: {
    type: "object";
    properties?: { [key: string]: object };
    required?: string[];
  };
  outputSchema?: {
    type: "object";
    properties?: { [key: string]: object };
    required?: string[];
  };
}
```

Rules:
1. `name` must be non-empty
2. `inputSchema.type` must be exactly `"object"`
3. `inputSchema.properties` values (if present) must be objects (maps), not primitives
4. Property `type` fields within `inputSchema.properties` (if present) must be valid JSON Schema types: `string`, `number`, `integer`, `boolean`, `array`, `object`, `null`
5. `inputSchema.required` entries (if present) must be strings
6. Same rules (2-5) apply to `outputSchema` if present

### RawInputSchema / RawOutputSchema

The `mcp.Tool` struct has `RawInputSchema json.RawMessage` as an alternative to the structured `InputSchema`. When `RawInputSchema` is populated, unmarshal it into a `map[string]any` and apply the same validation rules. If unmarshaling fails, the tool is invalid.

### What is not validated

- `$defs` and `$ref` usage — left as-is, too complex to validate without a full JSON Schema resolver
- `additionalProperties` — structural presence is accepted without deep validation

## Integration Point

Validation runs in `MCPManager.manage()` after `getTools()` fetches tools from the upstream server and before `diffTools()` computes adds/removes. Invalid tools never reach the gateway server.

```
getTools() -> validateTools() -> diffTools() -> AddTools/DeleteTools
```

The `InvalidToolPolicy` is stored as a field on `MCPManager` and passed via the constructor:

```go
func NewUpstreamMCPManager(
    upstream MCP,
    gatewaySever ToolsAdderDeleter,
    logger *slog.Logger,
    tickerInterval time.Duration,
    policy InvalidToolPolicy,
) *MCPManager
```

The broker passes the policy when creating managers in `OnConfigChange()`.

## Status Reporting

Extend `upstream.ServerValidationStatus` to surface invalid tool details:

```go
type InvalidToolInfo struct {
    Name   string   `json:"name"`
    Errors []string `json:"errors"`
}

type ServerValidationStatus struct {
    ID              string            `json:"id"`
    Name            string            `json:"name"`
    LastValidated   time.Time         `json:"lastValidated"`
    Message         string            `json:"message"`
    Ready           bool              `json:"ready"`
    TotalTools      int               `json:"totalTools"`
    InvalidTools    int               `json:"invalidTools"`
    InvalidToolList []InvalidToolInfo `json:"invalidToolList,omitempty"`
}
```

`TotalTools` remains the count of tools fetched from upstream (valid + invalid). `InvalidTools` is the count of tools that failed validation. The difference is the count of tools actually served.

Note: `broker.ServerValidationStatus` in `internal/broker/status.go` is a separate type used for detailed per-server queries. It does not need changes — the `upstream.ServerValidationStatus` is what flows through `StatusResponse.Servers`.

### FilterOut behavior

- `Ready: true` (unless ALL tools are invalid, then `Ready: false`)
- `TotalTools`: total fetched from upstream
- `InvalidTools`: count of invalid tools
- `InvalidToolList`: names and errors for each invalid tool
- `Message`: includes note about filtered tools
- Log warnings for each filtered tool

### RejectServer behavior

- `Ready: false`
- `TotalTools`: total fetched from upstream
- `InvalidTools`: count of invalid tools
- `InvalidToolList`: names and errors for each invalid tool
- `Message`: error explaining rejection due to invalid tools
- Log errors for rejected server

## Files Changed

| File | Change |
|------|--------|
| `cmd/mcp-broker-router/main.go` | Add `--invalid-tool-policy` CLI flag |
| `internal/broker/upstream/validate.go` | New file: validation logic, types (`InvalidToolPolicy`, `InvalidToolInfo`) |
| `internal/broker/upstream/validate_test.go` | New file: validation unit tests |
| `internal/broker/upstream/manager.go` | Integrate validation into `manage()`, extend `ServerValidationStatus`, add policy to constructor |
| `internal/broker/upstream/manager_test.go` | Test both policies with mock tools |
| `internal/broker/broker.go` | Accept policy, pass to `NewUpstreamMCPManager` |

## Test Plan

### Unit tests (`validate_test.go`)
- Valid tool passes validation
- Empty name fails
- `inputSchema.type` not `"object"` fails (e.g., `"int"`, `"string"`, empty)
- Property value that is a primitive (not a map) fails
- Property with invalid `type` field fails (e.g., `"int"` instead of `"integer"`)
- Invalid required entry type fails
- `outputSchema` validation rules mirror `inputSchema`
- Multiple errors collected per tool
- `RawInputSchema` is validated when present

### Unit tests (`manager_test.go`)
- FilterOut policy: invalid tools excluded, valid tools served, status shows invalid details
- FilterOut policy: all tools invalid marks server not ready
- RejectServer policy: any invalid tool causes all tools to be rejected, status not ready
- Mix of valid and invalid tools with FilterOut
- All valid tools: no change in behavior for either policy

### E2E
- Custom-response-server (already has `"type": "int"`) should have its tools filtered with default policy
- Verify `/status` endpoint reports invalid tool info

# Tool Schema Validation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Validate tool schemas from upstream MCP servers before serving them through the gateway, filtering or rejecting tools with invalid schemas.

**Architecture:** A validation function in `internal/broker/upstream/validate.go` checks each tool against the MCP Tool spec. `MCPManager` calls it after fetching tools and before diffing/adding. An `InvalidToolPolicy` flag (`FilterOut` or `RejectServer`) controls behavior. Status reporting surfaces invalid tool details.

**Tech Stack:** Go, mcp-go (`mcp.Tool`, `server.ServerTool`), existing test framework (testify, ginkgo/gomega)

**Spec:** `docs/superpowers/specs/2026-03-25-tool-schema-validation-design.md`

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `internal/broker/upstream/validate.go` | Create | `InvalidToolPolicy` type/constants, `InvalidToolInfo` type, `ValidateTool()` function, `ValidateTools()` batch function |
| `internal/broker/upstream/validate_test.go` | Create | Unit tests for all validation rules |
| `internal/broker/upstream/manager.go` | Modify | Add policy field to `MCPManager`, update constructor, integrate validation in `manage()`, extend `ServerValidationStatus` |
| `internal/broker/upstream/manager_test.go` | Modify | Add tests for both policies, update all `NewUpstreamMCPManager` calls with new policy param, fix mock tools to include `InputSchema.Type: "object"` |
| `internal/broker/broker.go` | Modify | Accept and store policy, pass to manager construction |
| `internal/broker/filtered_tools_handler_test.go` | Modify | Update `NewUpstreamMCPManager` call at line 53 with policy param |
| `internal/broker/status_test.go` | Modify | Update `NewUpstreamMCPManager` call at line 36 with policy param |
| `cmd/mcp-broker-router/main.go` | Modify | Add `--invalid-tool-policy` flag with validation, pass to broker |
| `tests/e2e/tool_validation_test.go` | Create | E2E test using custom-response-server (has `"type": "int"`) to verify filtering and `/status` endpoint |

**Note:** `internal/broker/status.go` has a separate `broker.ServerValidationStatus` type — it is NOT changed by this work. Only `upstream.ServerValidationStatus` is extended.

---

### Task 1: Create validation types and function

**Files:**
- Create: `internal/broker/upstream/validate.go`
- Create: `internal/broker/upstream/validate_test.go`

- [ ] **Step 1: Write the validation test file with tests for all rules**

```go
// internal/broker/upstream/validate_test.go
package upstream

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
)

func TestValidateTool(t *testing.T) {
	tests := []struct {
		name        string
		tool        mcp.Tool
		expectValid bool
		errContains string
	}{
		{
			name: "valid tool with properties",
			tool: mcp.Tool{
				Name: "my_tool",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"name": map[string]any{
							"type":        "string",
							"description": "a name",
						},
					},
					Required: []string{"name"},
				},
			},
			expectValid: true,
		},
		{
			name: "valid tool with no properties",
			tool: mcp.Tool{
				Name: "simple_tool",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
				},
			},
			expectValid: true,
		},
		{
			name: "empty name",
			tool: mcp.Tool{
				Name: "",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
				},
			},
			expectValid: false,
			errContains: "name must not be empty",
		},
		{
			name: "inputSchema type is not object",
			tool: mcp.Tool{
				Name: "bad_type",
				InputSchema: mcp.ToolInputSchema{
					Type: "string",
				},
			},
			expectValid: false,
			errContains: "inputSchema.type must be \"object\"",
		},
		{
			name: "inputSchema type is empty",
			tool: mcp.Tool{
				Name: "empty_type",
				InputSchema: mcp.ToolInputSchema{
					Type: "",
				},
			},
			expectValid: false,
			errContains: "inputSchema.type must be \"object\"",
		},
		{
			name: "property value is not an object",
			tool: mcp.Tool{
				Name: "bad_prop",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"name": "not-an-object",
					},
				},
			},
			expectValid: false,
			errContains: "inputSchema.properties[\"name\"] must be an object",
		},
		{
			name: "property has invalid type field",
			tool: mcp.Tool{
				Name: "bad_prop_type",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"code": map[string]any{
							"type": "int",
						},
					},
				},
			},
			expectValid: false,
			errContains: "inputSchema.properties[\"code\"].type \"int\" is not a valid JSON Schema type",
		},
		{
			name: "property type integer is valid",
			tool: mcp.Tool{
				Name: "good_integer",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"count": map[string]any{
							"type": "integer",
						},
					},
				},
			},
			expectValid: true,
		},
		{
			name: "all valid json schema types accepted",
			tool: mcp.Tool{
				Name: "all_types",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"a": map[string]any{"type": "string"},
						"b": map[string]any{"type": "number"},
						"c": map[string]any{"type": "integer"},
						"d": map[string]any{"type": "boolean"},
						"e": map[string]any{"type": "array"},
						"f": map[string]any{"type": "object"},
						"g": map[string]any{"type": "null"},
					},
				},
			},
			expectValid: true,
		},
		{
			name: "property with no type field is valid",
			tool: mcp.Tool{
				Name: "no_type_prop",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]any{
						"data": map[string]any{
							"description": "some data",
						},
					},
				},
			},
			expectValid: true,
		},
		{
			name: "multiple errors collected",
			tool: mcp.Tool{
				Name: "",
				InputSchema: mcp.ToolInputSchema{
					Type: "string",
				},
			},
			expectValid: false,
			errContains: "name must not be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := ValidateTool(tt.tool)
			if tt.expectValid {
				assert.Empty(t, info.Errors, "expected no errors")
			} else {
				assert.NotEmpty(t, info.Errors, "expected errors")
				found := false
				for _, e := range info.Errors {
					if strings.Contains(e, tt.errContains) {
						found = true
						break
					}
				}
				assert.True(t, found, "expected error containing %q, got %v", tt.errContains, info.Errors)
			}
		})
	}
}

func TestValidateTool_MultipleErrors(t *testing.T) {
	tool := mcp.Tool{
		Name: "",
		InputSchema: mcp.ToolInputSchema{
			Type: "string",
		},
	}
	info := ValidateTool(tool)
	assert.GreaterOrEqual(t, len(info.Errors), 2, "expected at least 2 errors for empty name + wrong type")
}

func TestValidateTools(t *testing.T) {
	tools := []mcp.Tool{
		{
			Name:        "valid_tool",
			InputSchema: mcp.ToolInputSchema{Type: "object"},
		},
		{
			Name:        "invalid_tool",
			InputSchema: mcp.ToolInputSchema{Type: "int"},
		},
		{
			Name: "another_valid",
			InputSchema: mcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"x": map[string]any{"type": "number"},
				},
			},
		},
	}

	valid, invalid := ValidateTools(tools)
	assert.Len(t, valid, 2)
	assert.Len(t, invalid, 1)
	assert.Equal(t, "invalid_tool", invalid[0].Name)
	assert.Equal(t, "valid_tool", valid[0].Name)
	assert.Equal(t, "another_valid", valid[1].Name)
}

func TestValidateTool_OutputSchema(t *testing.T) {
	tool := mcp.Tool{
		Name:         "with_output",
		InputSchema:  mcp.ToolInputSchema{Type: "object"},
		OutputSchema: mcp.ToolOutputSchema{Type: "string"},
	}

	info := ValidateTool(tool)
	assert.NotEmpty(t, info.Errors)
	assert.Contains(t, info.Errors[0], "outputSchema.type must be \"object\"")
}

func TestInvalidToolPolicyConstants(t *testing.T) {
	assert.Equal(t, InvalidToolPolicy("FilterOut"), InvalidToolPolicyFilterOut)
	assert.Equal(t, InvalidToolPolicy("RejectServer"), InvalidToolPolicyRejectServer)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway && go test ./internal/broker/upstream/ -run "TestValidateTool|TestValidateTools|TestInvalidToolPolicy" -v`
Expected: FAIL — `ValidateTool`, `ValidateTools`, `InvalidToolPolicy*` not defined

- [ ] **Step 3: Write the validation implementation**

```go
// internal/broker/upstream/validate.go
package upstream

import (
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// InvalidToolPolicy controls behavior when upstream tools have invalid schemas
type InvalidToolPolicy string

const (
	// InvalidToolPolicyFilterOut skips invalid tools and serves valid ones
	InvalidToolPolicyFilterOut InvalidToolPolicy = "FilterOut"
	// InvalidToolPolicyRejectServer rejects all tools if any are invalid
	InvalidToolPolicyRejectServer InvalidToolPolicy = "RejectServer"
)

// InvalidToolInfo contains validation errors for a single tool
type InvalidToolInfo struct {
	Name   string   `json:"name"`
	Errors []string `json:"errors"`
}

var validJSONSchemaTypes = map[string]bool{
	"string":  true,
	"number":  true,
	"integer": true,
	"boolean": true,
	"array":   true,
	"object":  true,
	"null":    true,
}

// ValidateTool validates a single tool against the MCP Tool schema.
// Returns an InvalidToolInfo with any errors found. If Errors is empty the tool is valid.
func ValidateTool(tool mcp.Tool) InvalidToolInfo {
	info := InvalidToolInfo{Name: tool.Name}

	if tool.Name == "" {
		info.Errors = append(info.Errors, "name must not be empty")
	}

	validateSchema(&info, "inputSchema", tool.InputSchema.Type, tool.InputSchema.Properties)

	if tool.OutputSchema.Type != "" || tool.OutputSchema.Properties != nil {
		validateSchema(&info, "outputSchema", tool.OutputSchema.Type, tool.OutputSchema.Properties)
	}

	return info
}

func validateSchema(info *InvalidToolInfo, prefix, schemaType string, properties map[string]any) {
	if schemaType != "object" {
		info.Errors = append(info.Errors, fmt.Sprintf("%s.type must be \"object\", got %q", prefix, schemaType))
	}

	for propName, propValue := range properties {
		propMap, ok := propValue.(map[string]any)
		if !ok {
			info.Errors = append(info.Errors, fmt.Sprintf("%s.properties[%q] must be an object, got %T", prefix, propName, propValue))
			continue
		}
		typeVal, hasType := propMap["type"]
		if !hasType {
			continue
		}
		typeStr, ok := typeVal.(string)
		if !ok {
			info.Errors = append(info.Errors, fmt.Sprintf("%s.properties[%q].type must be a string, got %T", prefix, propName, typeVal))
			continue
		}
		if !validJSONSchemaTypes[typeStr] {
			info.Errors = append(info.Errors, fmt.Sprintf("%s.properties[%q].type %q is not a valid JSON Schema type", prefix, propName, typeStr))
		}
	}
}

// ValidateTools validates a list of tools and returns the valid tools and info about invalid ones.
func ValidateTools(tools []mcp.Tool) ([]mcp.Tool, []InvalidToolInfo) {
	var valid []mcp.Tool
	var invalid []InvalidToolInfo

	for _, tool := range tools {
		info := ValidateTool(tool)
		if len(info.Errors) > 0 {
			invalid = append(invalid, info)
		} else {
			valid = append(valid, tool)
		}
	}

	return valid, invalid
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway && go test ./internal/broker/upstream/ -run "TestValidateTool|TestValidateTools|TestInvalidToolPolicy" -v`
Expected: PASS

- [ ] **Step 5: Run lint**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway && make lint`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/broker/upstream/validate.go internal/broker/upstream/validate_test.go
git commit -m "feat: add tool schema validation logic (#662)"
```

---

### Task 2: Integrate validation into MCPManager

**Files:**
- Modify: `internal/broker/upstream/manager.go:44-52` — extend `ServerValidationStatus`
- Modify: `internal/broker/upstream/manager.go:92` — add policy field
- Modify: `internal/broker/upstream/manager.go:98-117` — update constructor
- Modify: `internal/broker/upstream/manager.go:180-255` — integrate validation in `manage()`
- Modify: `internal/broker/upstream/manager.go:276-288` — update `setStatus`
- Modify: `internal/broker/upstream/manager_test.go` — update all 19 constructor calls, fix mock tools, add policy tests

**IMPORTANT**: The `setStatus` signature changes from 2 to 3 arguments. ALL callers must be updated — there are 5 calls in `manage()` (lines 190, 200, 214, 222, 254). The existing `TestMCPManager_setStatus` test (line 332) also needs updating.

**IMPORTANT**: Existing mock tools like `{Name: "mock_tool"}` and `{Name: "tool1"}` have no `InputSchema.Type` set (defaults to `""`). After validation, these would be filtered as invalid. All mock tools used in tests that go through `manage()` must include `InputSchema: mcp.ToolInputSchema{Type: "object"}`.

- [ ] **Step 1: Write tests for FilterOut and RejectServer policies**

Add to the end of `internal/broker/upstream/manager_test.go`:

```go
func TestMCPManager_manage_FilterOutPolicy(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.tools = []mcp.Tool{
		{
			Name:        "valid_tool",
			InputSchema: mcp.ToolInputSchema{Type: "object"},
		},
		{
			Name:        "invalid_tool",
			InputSchema: mcp.ToolInputSchema{Type: "int"},
		},
	}
	mock.hasToolsCap = false
	gateway := newMockToolsAdderDeleter()
	manager := NewUpstreamMCPManager(mock, gateway, logger, 0, InvalidToolPolicyFilterOut)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.True(t, status.Ready)
	assert.Equal(t, 2, status.TotalTools)
	assert.Equal(t, 1, status.InvalidTools)
	assert.Len(t, status.InvalidToolList, 1)
	assert.Equal(t, "invalid_tool", status.InvalidToolList[0].Name)

	// only valid tool should be in gateway
	assert.Len(t, gateway.tools, 1)
	assert.Contains(t, gateway.tools, "test_valid_tool")
}

func TestMCPManager_manage_FilterOutPolicy_AllInvalid(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.tools = []mcp.Tool{
		{
			Name:        "bad1",
			InputSchema: mcp.ToolInputSchema{Type: "int"},
		},
		{
			Name:        "bad2",
			InputSchema: mcp.ToolInputSchema{Type: "string"},
		},
	}
	mock.hasToolsCap = false
	gateway := newMockToolsAdderDeleter()
	manager := NewUpstreamMCPManager(mock, gateway, logger, 0, InvalidToolPolicyFilterOut)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.False(t, status.Ready)
	assert.Equal(t, 2, status.TotalTools)
	assert.Equal(t, 2, status.InvalidTools)
	assert.Len(t, gateway.tools, 0)
}

func TestMCPManager_manage_RejectServerPolicy(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.tools = []mcp.Tool{
		{
			Name:        "valid_tool",
			InputSchema: mcp.ToolInputSchema{Type: "object"},
		},
		{
			Name:        "invalid_tool",
			InputSchema: mcp.ToolInputSchema{Type: "int"},
		},
	}
	mock.hasToolsCap = false
	gateway := newMockToolsAdderDeleter()
	manager := NewUpstreamMCPManager(mock, gateway, logger, 0, InvalidToolPolicyRejectServer)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.False(t, status.Ready)
	assert.Equal(t, 2, status.TotalTools)
	assert.Equal(t, 1, status.InvalidTools)
	assert.Len(t, gateway.tools, 0)
}

func TestMCPManager_manage_AllValidTools(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := newMockMCP("test-server", "test_")
	mock.tools = []mcp.Tool{
		{
			Name:        "tool1",
			InputSchema: mcp.ToolInputSchema{Type: "object"},
		},
		{
			Name:        "tool2",
			InputSchema: mcp.ToolInputSchema{Type: "object"},
		},
	}
	mock.hasToolsCap = false
	gateway := newMockToolsAdderDeleter()
	manager := NewUpstreamMCPManager(mock, gateway, logger, 0, InvalidToolPolicyFilterOut)

	manager.manage(context.Background(), eventTypeTimer)

	status := manager.GetStatus()
	assert.True(t, status.Ready)
	assert.Equal(t, 2, status.TotalTools)
	assert.Equal(t, 0, status.InvalidTools)
	assert.Len(t, gateway.tools, 2)
}
```

- [ ] **Step 2: Update ALL existing `NewUpstreamMCPManager` calls in `manager_test.go` to pass policy**

There are 19 call sites. Every one needs `InvalidToolPolicyFilterOut` as the last argument. For example:

```go
// Before:
manager := NewUpstreamMCPManager(mock, gateway, logger, 0)
// After:
manager := NewUpstreamMCPManager(mock, gateway, logger, 0, InvalidToolPolicyFilterOut)
```

Use search-and-replace across the file. Every `NewUpstreamMCPManager(` call in this file must get the extra arg.

- [ ] **Step 3: Fix mock tools in existing tests that go through `manage()`**

Tests that call `manage()` and provide mock tools need `InputSchema.Type: "object"` on each tool. Update the `newMockMCP` function's default tools (line 107):

```go
// Before:
tools: []mcp.Tool{{Name: "mock_tool"}},
// After:
tools: []mcp.Tool{{Name: "mock_tool", InputSchema: mcp.ToolInputSchema{Type: "object"}}},
```

Also update all inline tool definitions in tests that go through `manage()`. Search for `mcp.Tool{Name:` in tests like `TestMCPManager_manage_Success`, `TestMCPManager_manage_SkipsFetchOnTimerWhenToolsListChangeSupported`, `TestMCPManager_manage_OnlyCallsAddDeleteWhenNeeded`, `TestServerToolsManagement`. Each `mcp.Tool{Name: "toolN"}` needs `InputSchema: mcp.ToolInputSchema{Type: "object"}` added.

- [ ] **Step 4: Run tests to verify they fail (constructor signature mismatch)**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway && go test ./internal/broker/upstream/ -v 2>&1 | head -20`
Expected: FAIL — `NewUpstreamMCPManager` has wrong number of arguments

- [ ] **Step 5: Extend `ServerValidationStatus` in `manager.go`**

In `internal/broker/upstream/manager.go`, update the struct at lines 44-52:

```go
// ServerValidationStatus contains the validation results for an upstream MCP server
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

- [ ] **Step 6: Add policy field to `MCPManager` and update constructor**

In `internal/broker/upstream/manager.go`, add a `policy` field to `MCPManager` struct (after line 92, after `status`):

```go
	policy   InvalidToolPolicy
```

Update `NewUpstreamMCPManager` at lines 98-117 to accept and store the policy:

```go
func NewUpstreamMCPManager(upstream MCP, gatewaySever ToolsAdderDeleter, logger *slog.Logger, tickerInterval time.Duration, policy InvalidToolPolicy) *MCPManager {
	if tickerInterval <= 0 {
		tickerInterval = DefaultTickerInterval
	}

	return &MCPManager{
		MCP:            upstream,
		gatewayServer:  gatewaySever,
		tickerInterval: tickerInterval,
		ticker:         time.NewTicker(tickerInterval),
		logger:         logger,
		done:           make(chan struct{}),
		toolsMap:       map[string]mcp.Tool{},
		servedToolsMap: map[string]mcp.Tool{},
		serverTools:    []server.ServerTool{},
		policy:         policy,
	}
}
```

- [ ] **Step 7: Update `setStatus` to accept and store invalid tool info**

Replace `setStatus` at lines 276-288. Note: the signature changes from 2 to 3 args.

```go
func (man *MCPManager) setStatus(err error, totalTools int, invalidTools []InvalidToolInfo) {
	man.status.ID = string(man.MCP.ID())
	man.status.LastValidated = time.Now()
	man.status.Name = man.MCPName()
	man.status.TotalTools = totalTools
	man.status.InvalidTools = len(invalidTools)
	man.status.InvalidToolList = invalidTools
	if err != nil {
		man.status.Message = err.Error()
		man.status.Ready = false
		return
	}
	man.status.Ready = true
	if len(invalidTools) > 0 {
		man.status.Message = fmt.Sprintf("server added successfully. Total tools added %d. %d tools filtered due to invalid schemas", len(man.serverTools), len(invalidTools))
	} else {
		man.status.Message = fmt.Sprintf("server added successfully. Total tools added %d", len(man.serverTools))
	}
}
```

- [ ] **Step 8: Update ALL existing `setStatus` callers in `manage()`**

There are 5 calls to `setStatus` in `manage()`. The ones at lines 190 and 200 (connect/ping errors) are NOT in the replacement block from Step 9. Update them to pass `nil`:

```go
// line 190 (connect error):
man.setStatus(err, numberOfTools, nil)

// line 200 (ping error):
man.setStatus(err, numberOfTools, nil)
```

Also update `TestMCPManager_setStatus` test (around line 332) — each call needs a third argument:

```go
// Before:
manager.setStatus(tc.err, tc.totalTools)
// After:
manager.setStatus(tc.err, tc.totalTools, nil)
```

- [ ] **Step 9: Integrate validation into `manage()` method**

In `internal/broker/upstream/manager.go`, replace lines 209-254 (from `man.logger.Debug("fetching tools"...` through `man.setStatus(nil, numberOfTools)`) with:

```go
	man.logger.Debug("fetching tools", "upstream mcp server", man.MCP.ID())
	current, fetched, err := man.getTools(ctx)
	if err != nil {
		err = fmt.Errorf("upstream mcp failed to list tools server %s : %w", man.MCP.ID(), err)
		man.logger.Error("failed to list tools", "upstream mcp server", man.MCP.ID(), "error", err)
		man.setStatus(err, len(fetched), nil)
		return
	}

	// validate tools against MCP schema
	validTools, invalidTools := ValidateTools(fetched)
	totalFetched := len(fetched)
	for _, invalid := range invalidTools {
		man.logger.Warn("invalid tool schema filtered", "upstream mcp server", man.MCP.ID(), "tool", invalid.Name, "errors", invalid.Errors)
	}

	if len(invalidTools) > 0 && man.policy == InvalidToolPolicyRejectServer {
		err = fmt.Errorf("upstream mcp %s rejected: %d of %d tools have invalid schemas", man.MCP.ID(), len(invalidTools), totalFetched)
		man.logger.Error("rejecting server due to invalid tools", "upstream mcp server", man.MCP.ID(), "error", err)
		man.removeAllTools()
		man.setStatus(err, totalFetched, invalidTools)
		return
	}

	if len(invalidTools) > 0 && len(validTools) == 0 {
		err = fmt.Errorf("upstream mcp %s: all %d tools have invalid schemas", man.MCP.ID(), totalFetched)
		man.logger.Error("all tools invalid", "upstream mcp server", man.MCP.ID(), "error", err)
		man.removeAllTools()
		man.setStatus(err, totalFetched, invalidTools)
		return
	}

	// always compare the tools without prefix
	toAdd, toRemove := man.diffTools(current, validTools)
	if err := man.findToolConflicts(toAdd); err != nil {
		err = fmt.Errorf("upstream mcp failed to add tools to gateway %s : %w", man.MCP.ID(), err)
		man.logger.Error("tool conflict detected", "upstream mcp server", man.MCP.ID(), "error", err)
		man.setStatus(err, totalFetched, invalidTools)
		return
	}
	man.toolsLock.Lock()
	man.tools = validTools
	numberOfTools = totalFetched
	// set a tools map for quick look up by other functions
	man.toolsMap = map[string]mcp.Tool{}
	man.servedToolsMap = map[string]mcp.Tool{}
	// we always use any prefix here as it is what the client will call
	for _, newTool := range validTools {
		man.toolsMap[newTool.Name] = newTool
		toolName := prefixedName(man.MCP.GetPrefix(), newTool.Name)
		man.servedToolsMap[toolName] = newTool
	}
	// serverTools will have the prefix if one is set
	man.logger.Debug("updating gateway tools", "upstream mcp server", man.MCP.ID(), "adding", len(toAdd), "removing", len(toRemove))
	if len(toRemove) > 0 {
		man.gatewayServer.DeleteTools(toRemove...)
	}
	if len(toAdd) > 0 {
		man.gatewayServer.AddTools(toAdd...)
	}

	// rebuild our internal tools
	man.serverTools = slices.DeleteFunc(man.serverTools, func(tool server.ServerTool) bool {
		return slices.Contains(toRemove, tool.Tool.Name)
	})

	man.serverTools = append(man.serverTools, toAdd...)
	man.logger.Debug("internal tools", "upstream mcp server", man.MCP.ID(), "total", len(man.serverTools))
	man.toolsLock.Unlock()
	man.setStatus(nil, totalFetched, invalidTools)
```

- [ ] **Step 10: Run tests to verify they pass**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway && go test ./internal/broker/upstream/ -v`
Expected: PASS (all existing + new tests)

- [ ] **Step 11: Run lint**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway && make lint`
Expected: PASS

- [ ] **Step 12: Commit**

```bash
git add internal/broker/upstream/manager.go internal/broker/upstream/manager_test.go
git commit -m "feat: integrate tool schema validation into MCPManager (#662)"
```

---

### Task 3: Wire policy through broker, CLI flag, and fix external callers

**Files:**
- Modify: `internal/broker/broker.go:52-98` — add policy field and `WithInvalidToolPolicy` option
- Modify: `internal/broker/broker.go:107` — set default
- Modify: `internal/broker/broker.go:182` — pass policy to manager construction
- Modify: `internal/broker/filtered_tools_handler_test.go:53` — update constructor call
- Modify: `internal/broker/status_test.go:36` — update constructor call
- Modify: `cmd/mcp-broker-router/main.go:72` — add flag variable
- Modify: `cmd/mcp-broker-router/main.go:136` — register flag
- Modify: `cmd/mcp-broker-router/main.go:206` — pass policy to broker setup
- Modify: `cmd/mcp-broker-router/main.go:284` — update `setUpBroker` signature

- [ ] **Step 1: Update external callers of `NewUpstreamMCPManager` in broker package**

In `internal/broker/filtered_tools_handler_test.go`, update line 53:

```go
// Before:
manager := upstream.NewUpstreamMCPManager(mcpServer, nil, slog.Default(), 0)
// After:
manager := upstream.NewUpstreamMCPManager(mcpServer, nil, slog.Default(), 0, upstream.InvalidToolPolicyFilterOut)
```

In `internal/broker/status_test.go`, update line 36:

```go
// Before:
manager := upstream.NewUpstreamMCPManager(mcpServer, nil, slog.Default(), 0)
// After:
manager := upstream.NewUpstreamMCPManager(mcpServer, nil, slog.Default(), 0, upstream.InvalidToolPolicyFilterOut)
```

- [ ] **Step 2: Add policy field and option to broker**

In `internal/broker/broker.go`, add a field to `mcpBrokerImpl` struct (after line 68, after `trustedHeadersPublicKey`):

```go
	// invalidToolPolicy controls behavior when upstream tools have invalid schemas
	invalidToolPolicy upstream.InvalidToolPolicy
```

Add a new option function after `WithManagerTickerInterval` (after line 98):

```go
// WithInvalidToolPolicy sets the policy for handling upstream tools with invalid schemas
func WithInvalidToolPolicy(policy upstream.InvalidToolPolicy) func(mb *mcpBrokerImpl) {
	return func(mb *mcpBrokerImpl) {
		mb.invalidToolPolicy = policy
	}
}
```

Set the default in `NewBroker` struct literal at line 107, after `managerTickerInterval`:

```go
		invalidToolPolicy:     upstream.InvalidToolPolicyFilterOut,
```

- [ ] **Step 3: Pass policy to manager construction in `OnConfigChange`**

In `internal/broker/broker.go`, update line 182:

```go
// Before:
manager := upstream.NewUpstreamMCPManager(upstream.NewUpstreamMCP(mcpServer), m.listeningMCPServer, m.logger.With("sub-component", "mcp-manager"), m.managerTickerInterval)
// After:
manager := upstream.NewUpstreamMCPManager(upstream.NewUpstreamMCP(mcpServer), m.listeningMCPServer, m.logger.With("sub-component", "mcp-manager"), m.managerTickerInterval, m.invalidToolPolicy)
```

- [ ] **Step 4: Add CLI flag in `main.go`**

In `cmd/mcp-broker-router/main.go`, add import (after line 22):

```go
	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
```

Add flag variable after `enforceToolFilteringFlag` (line 72):

```go
	invalidToolPolicyFlag string
```

Add flag registration before `flag.Parse()` (after line 136):

```go
	flag.StringVar(&invalidToolPolicyFlag, "invalid-tool-policy", "FilterOut", "policy for tools with invalid schemas: FilterOut (default) or RejectServer")
```

Add flag validation after `flag.Parse()` (after line 137):

```go
	if invalidToolPolicyFlag != string(upstream.InvalidToolPolicyFilterOut) && invalidToolPolicyFlag != string(upstream.InvalidToolPolicyRejectServer) {
		log.Fatalf("invalid value for --invalid-tool-policy: %q. Must be FilterOut or RejectServer", invalidToolPolicyFlag)
	}
```

- [ ] **Step 5: Pass policy through `setUpBroker`**

Update the `setUpBroker` function signature at line 284:

```go
func setUpBroker(address string, toolFiltering bool, sessionManager *session.JWTManager, writeTimeoutSecs int64, managerTickerInterval time.Duration, invalidToolPolicy upstream.InvalidToolPolicy) (*http.Server, broker.MCPBroker, *server.StreamableHTTPServer) {
```

Add the broker option at line 314 (inside the `broker.NewBroker` call):

```go
		broker.WithInvalidToolPolicy(invalidToolPolicy),
```

Update the call site at line 206:

```go
	brokerServer, mcpBroker, mcpServer := setUpBroker(mcpBrokerAddrFlag, enforceToolFilteringFlag, jwtSessionMgr, brokerWriteTimeoutSecs, managerTickerInterval, upstream.InvalidToolPolicy(invalidToolPolicyFlag))
```

- [ ] **Step 6: Build to verify compilation**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway && go build ./cmd/mcp-broker-router/`
Expected: PASS

- [ ] **Step 7: Run all broker tests**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway && go test ./internal/broker/... -v`
Expected: PASS

- [ ] **Step 8: Run lint**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway && make lint`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/broker/broker.go internal/broker/filtered_tools_handler_test.go internal/broker/status_test.go cmd/mcp-broker-router/main.go
git commit -m "feat: add --invalid-tool-policy CLI flag (#662)"
```

---

### Task 4: E2E test for invalid tool schema filtering

The custom-response-server at `tests/servers/custom-response-server/main.go` already presents a tool with `"type": "int"` (an invalid JSON Schema type). It is deployed in the test cluster as `mcp-custom-response` in the `mcp-test` namespace with an HTTPRoute `mcp-custom-response-route`. This test registers that server and verifies its invalid tool is filtered and the status endpoint reports it.

**Files:**
- Create: `tests/e2e/tool_validation_test.go`

- [ ] **Step 1: Write the E2E test**

```go
//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/mark3labs/mcp-go/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Tool Schema Validation", func() {
	var (
		testResources    = []client.Object{}
		mcpGatewayClient *NotifyingMCPClient
	)

	BeforeEach(func() {
		Eventually(func(g Gomega) {
			var err error
			mcpGatewayClient, err = NewMCPGatewayClientWithNotifications(ctx, gatewayURL, nil)
			g.Expect(err).NotTo(HaveOccurred())
		}, TestTimeoutMedium, TestRetryInterval).Should(Succeed())
	})

	AfterEach(func() {
		if mcpGatewayClient != nil {
			mcpGatewayClient.Close()
			mcpGatewayClient = nil
		}
		for _, to := range testResources {
			CleanupResource(ctx, k8sClient, to)
		}
		testResources = []client.Object{}
	})

	It("filters tools with invalid schemas from custom-response-server", func() {
		By("Registering the custom-response-server which has type:int (invalid)")
		registration := NewTestResources("schema-validation", k8sClient).
			ForInternalService("mcp-custom-response", 9090).
			WithToolPrefix("custom_resp_").
			Build()
		testResources = append(testResources, registration.GetObjects()...)
		registeredServer := registration.Register(ctx)

		By("Waiting for the MCPServerRegistration to be processed")
		Eventually(func(g Gomega) {
			g.Expect(VerifyMCPServerRegistrationHasCondition(ctx, k8sClient, registeredServer.Name, registeredServer.Namespace)).To(BeNil())
		}, TestTimeoutConfigSync, TestRetryInterval).Should(Succeed())

		By("Verifying that tools with custom_resp_ prefix are NOT present (filtered out)")
		// The custom-response-server only has one tool with "type": "int" which should be filtered
		Eventually(func(g Gomega) {
			toolsList, err := mcpGatewayClient.ListTools(ctx, mcp.ListToolsRequest{})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(toolsList).NotTo(BeNil())
			g.Expect(ToolsListHasPrefix(toolsList, "custom_resp_")).To(BeFalse(),
				"tools with prefix custom_resp_ should be filtered due to invalid schema")
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())

		By("Verifying the /status endpoint reports invalid tools")
		statusURL := strings.Replace(gatewayURL, "/mcp", "/status", 1)
		Eventually(func(g Gomega) {
			resp, err := http.Get(statusURL)
			g.Expect(err).NotTo(HaveOccurred())
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			g.Expect(err).NotTo(HaveOccurred())

			var statusResp struct {
				Servers []upstream.ServerValidationStatus `json:"servers"`
			}
			g.Expect(json.Unmarshal(body, &statusResp)).To(Succeed())

			// find our server in the status response
			found := false
			for _, s := range statusResp.Servers {
				if strings.Contains(s.ID, "mcp-custom-response") {
					found = true
					g.Expect(s.InvalidTools).To(BeNumerically(">", 0),
						fmt.Sprintf("expected invalid tools > 0, status: %+v", s))
					g.Expect(s.InvalidToolList).NotTo(BeEmpty())
					break
				}
			}
			g.Expect(found).To(BeTrue(), "custom-response server not found in /status response. Body: %s", string(body))
		}, TestTimeoutLong, TestRetryInterval).Should(Succeed())
	})
})
```

- [ ] **Step 2: Verify compilation**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway && go build -tags=e2e ./tests/e2e/...`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add tests/e2e/tool_validation_test.go
git commit -m "test: add e2e test for tool schema validation (#662)"
```

---

### Task 5: Final verification

- [ ] **Step 1: Run all unit tests**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway && make test-unit`
Expected: PASS

- [ ] **Step 2: Run lint**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway && make lint`
Expected: PASS

- [ ] **Step 3: Verify full build**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway && go build ./...`
Expected: PASS

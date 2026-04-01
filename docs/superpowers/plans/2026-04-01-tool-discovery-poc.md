# Tool Discovery POC Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `discover_tools` and `select_tools` meta-tools to the broker, with session-scoped tool filtering and router awareness of broker-internal tools.

**Architecture:** Two new tools registered on the broker's `listeningMCPServer` via `AddTools`. `discover_tools` returns lightweight metadata (category, hint, tool names) from registered upstream servers. `select_tools` stores a per-session tool scope and sends `notifications/tools/list_changed`. The `FilterTools` hook applies session scoping. The router recognizes broker tools via `IsBrokerTool()` and passes them through without upstream routing.

**Tech Stack:** Go, mcp-go SDK (`server.ServerTool`, `ToolHandlerFunc`), existing broker/router infrastructure.

---

## File Structure

- **Create:** `internal/broker/discovery.go` — `discover_tools` and `select_tools` tool definitions, handlers, session scope store, `IsBrokerTool()` helper
- **Create:** `internal/broker/discovery_test.go` — unit tests for discovery handlers and session scoping
- **Modify:** `internal/broker/broker.go` — register meta-tools in `NewBroker()`, expose `IsBrokerTool()` on `MCPBroker` interface
- **Modify:** `internal/broker/filtered_tools_handler.go` — add session scope filter step in `FilterTools`
- **Modify:** `internal/config/types.go` — add `Category` and `Hint` fields to `MCPServer`
- **Modify:** `internal/mcp-router/request_handlers.go` — check `IsBrokerTool()` in `HandleToolCall`, pass through to broker if true

---

### Task 1: Add Category and Hint to MCPServer config

**Files:**
- Modify: `internal/config/types.go:59-67`

- [ ] **Step 1: Write the test**

No dedicated test needed — these are struct fields. Verified by compilation and downstream usage.

- [ ] **Step 2: Add fields to MCPServer**

In `internal/config/types.go`, add `Category` and `Hint` to the `MCPServer` struct:

```go
type MCPServer struct {
	Name       string      `json:"name"                 yaml:"name"`
	URL        string      `json:"url"                  yaml:"url"`
	Hostname   string      `json:"hostname,omitempty"   yaml:"hostname,omitempty"`
	ToolPrefix string      `json:"toolPrefix,omitempty" yaml:"toolPrefix,omitempty"`
	Auth       *AuthConfig `json:"auth,omitempty"       yaml:"auth,omitempty"`
	Credential string      `json:"credential,omitempty" yaml:"credential,omitempty"`
	Enabled    bool        `json:"enabled"              yaml:"enabled"`
	Category   string      `json:"category,omitempty"   yaml:"category,omitempty"`
	Hint       string      `json:"hint,omitempty"       yaml:"hint,omitempty"`
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./...`
Expected: clean compilation

- [ ] **Step 4: Commit**

```bash
git add internal/config/types.go
git commit -s -m "add category and hint fields to MCPServer config"
```

---

### Task 2: Create discovery.go with session scope store and IsBrokerTool

**Files:**
- Create: `internal/broker/discovery.go`
- Create: `internal/broker/discovery_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/broker/discovery_test.go`:

```go
package broker

import (
	"testing"
)

func TestIsBrokerTool(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		want     bool
	}{
		{"discover_tools is broker tool", "discover_tools", true},
		{"select_tools is broker tool", "select_tools", true},
		{"regular tool is not broker tool", "test1_greet", false},
		{"empty string is not broker tool", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsBrokerTool(tt.toolName); got != tt.want {
				t.Errorf("IsBrokerTool(%q) = %v, want %v", tt.toolName, got, tt.want)
			}
		})
	}
}

func TestSessionScope(t *testing.T) {
	store := newSessionScopeStore()

	t.Run("no scope returns nil", func(t *testing.T) {
		tools := store.getScope("session-1")
		if tools != nil {
			t.Errorf("expected nil, got %v", tools)
		}
	})

	t.Run("set and get scope", func(t *testing.T) {
		store.setScope("session-1", []string{"tool_a", "tool_b"})
		tools := store.getScope("session-1")
		if len(tools) != 2 {
			t.Fatalf("expected 2 tools, got %d", len(tools))
		}
		if tools["tool_a"] != true || tools["tool_b"] != true {
			t.Errorf("unexpected scope: %v", tools)
		}
	})

	t.Run("empty list clears scope", func(t *testing.T) {
		store.setScope("session-1", []string{})
		tools := store.getScope("session-1")
		if tools != nil {
			t.Errorf("expected nil after clear, got %v", tools)
		}
	})

	t.Run("remove session", func(t *testing.T) {
		store.setScope("session-2", []string{"tool_c"})
		store.removeSession("session-2")
		if store.getScope("session-2") != nil {
			t.Error("expected nil after remove")
		}
	})
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway/.claude/worktrees/tool-discovery-proposal && go test ./internal/broker/ -run "TestIsBrokerTool|TestSessionScope" -v`
Expected: FAIL — `IsBrokerTool` and `newSessionScopeStore` not defined

- [ ] **Step 3: Write discovery.go**

Create `internal/broker/discovery.go`:

```go
package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	discoverToolsName = "discover_tools"
	selectToolsName   = "select_tools"
)

// IsBrokerTool returns true if the tool is handled by the broker directly
func IsBrokerTool(name string) bool {
	return name == discoverToolsName || name == selectToolsName
}

// sessionScopeStore tracks per-session tool scoping
type sessionScopeStore struct {
	mu     sync.RWMutex
	scopes map[string]map[string]bool // sessionID -> set of tool names
}

func newSessionScopeStore() *sessionScopeStore {
	return &sessionScopeStore{
		scopes: make(map[string]map[string]bool),
	}
}

// getScope returns the scoped tool set for a session, or nil if no scope is set
func (s *sessionScopeStore) getScope(sessionID string) map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.scopes[sessionID]
}

// setScope sets the tool scope for a session. Empty list clears the scope.
func (s *sessionScopeStore) setScope(sessionID string, tools []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(tools) == 0 {
		delete(s.scopes, sessionID)
		return
	}
	scope := make(map[string]bool, len(tools))
	for _, t := range tools {
		scope[t] = true
	}
	s.scopes[sessionID] = scope
}

// removeSession cleans up scope when a session ends
func (s *sessionScopeStore) removeSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.scopes, sessionID)
}

// serverInfo is the lightweight metadata returned by discover_tools
type serverInfo struct {
	Name     string   `json:"name"`
	Category string   `json:"category"`
	Hint     string   `json:"hint,omitempty"`
	Tools    []string `json:"tools"`
}

type discoverToolsResponse struct {
	Servers []serverInfo `json:"servers"`
}

// discoveryTools returns the two meta-tool definitions
func discoveryTools(broker *mcpBrokerImpl) []server.ServerTool {
	return []server.ServerTool{
		{
			Tool: mcp.Tool{
				Name:        discoverToolsName,
				Description: "Discover available tool categories and servers. Returns lightweight metadata (categories, tool names, hints) to help you identify which tools are relevant to your current task. Use this before select_tools to narrow your working set.",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]map[string]any{
						"category": {
							"type":        "string",
							"description": "Optional category filter to narrow results",
						},
					},
				},
			},
			Handler: broker.handleDiscoverTools,
		},
		{
			Tool: mcp.Tool{
				Name:        selectToolsName,
				Description: "Scope your session to a specific set of tools. After calling this, your tools/list will only return the selected tools. Call discover_tools first to identify relevant tools. Call again with a different set to re-scope, or with an empty list to reset to the full tool set.",
				InputSchema: mcp.ToolInputSchema{
					Type: "object",
					Properties: map[string]map[string]any{
						"tools": {
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "List of tool names to include in your session scope",
						},
					},
					Required: []string{"tools"},
				},
			},
			Handler: broker.handleSelectTools,
		},
	}
}

func (broker *mcpBrokerImpl) handleDiscoverTools(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	broker.mcpLock.RLock()
	defer broker.mcpLock.RUnlock()

	categoryFilter, _ := req.GetArguments()["category"].(string)

	var servers []serverInfo
	for _, manager := range broker.mcpServers {
		conf := manager.MCP.GetConfig()
		category := conf.Category
		if category == "" {
			category = "uncategorised"
		}
		if categoryFilter != "" && category != categoryFilter {
			continue
		}

		var toolNames []string
		for _, tool := range manager.GetManagedTools() {
			toolNames = append(toolNames, fmt.Sprintf("%s%s", manager.MCP.GetPrefix(), tool.Name))
		}

		servers = append(servers, serverInfo{
			Name:     conf.Name,
			Category: category,
			Hint:     conf.Hint,
			Tools:    toolNames,
		})
	}

	resp := discoverToolsResponse{Servers: servers}
	data, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal discover response: %w", err)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(string(data)),
		},
	}, nil
}

func (broker *mcpBrokerImpl) handleSelectTools(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	toolsRaw, ok := args["tools"]
	if !ok {
		return nil, fmt.Errorf("missing required parameter: tools")
	}

	toolsSlice, ok := toolsRaw.([]any)
	if !ok {
		return nil, fmt.Errorf("tools must be an array of strings")
	}

	var tools []string
	for _, t := range toolsSlice {
		name, ok := t.(string)
		if !ok {
			return nil, fmt.Errorf("each tool must be a string")
		}
		tools = append(tools, name)
	}

	// validate all tools exist
	if len(tools) > 0 {
		broker.mcpLock.RLock()
		allTools := broker.listeningMCPServer.ListTools()
		broker.mcpLock.RUnlock()
		allToolSet := make(map[string]bool, len(allTools))
		for name := range allTools {
			allToolSet[name] = true
		}
		for _, name := range tools {
			if !allToolSet[name] {
				return nil, fmt.Errorf("tool %q does not exist or is not authorized", name)
			}
		}
	}

	session := server.ClientSessionFromContext(ctx)
	if session == nil {
		return nil, fmt.Errorf("no active session")
	}

	broker.sessionScopes.setScope(session.SessionID(), tools)

	// send tools/list_changed notification to this client
	if err := broker.listeningMCPServer.SendNotificationToClient(ctx, "notifications/tools/list_changed", nil); err != nil {
		slog.Error("failed to send tools/list_changed notification", "error", err)
	}

	if len(tools) == 0 {
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				mcp.NewTextContent("Session scope cleared. All tools are now available."),
			},
		}, nil
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.NewTextContent(fmt.Sprintf("Session scoped to %d tools. Call tools/list to see the updated set.", len(tools))),
		},
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway/.claude/worktrees/tool-discovery-proposal && go test ./internal/broker/ -run "TestIsBrokerTool|TestSessionScope" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/broker/discovery.go internal/broker/discovery_test.go
git commit -s -m "add discovery meta-tools and session scope store"
```

---

### Task 3: Register meta-tools in broker and add session scope cleanup

**Files:**
- Modify: `internal/broker/broker.go:53-78` (add `sessionScopes` field)
- Modify: `internal/broker/broker.go:115-159` (register tools in `NewBroker`, cleanup on session unregister)
- Modify: `internal/broker/broker.go:22-50` (add `IsBrokerTool` to interface)

- [ ] **Step 1: Add sessionScopes to mcpBrokerImpl**

In `internal/broker/broker.go`, add the `sessionScopes` field to the struct:

```go
type mcpBrokerImpl struct {
	virtualServers map[string]*config.VirtualServer
	vsLock         sync.RWMutex

	mcpServers map[config.UpstreamMCPID]*upstream.MCPManager
	mcpLock    sync.RWMutex

	listeningMCPServer *server.MCPServer

	logger *slog.Logger

	enforceToolFilter       bool
	trustedHeadersPublicKey string
	managerTickerInterval   time.Duration
	invalidToolPolicy       mcpv1alpha1.InvalidToolPolicy

	// sessionScopes tracks per-session tool scoping for discovery
	sessionScopes *sessionScopeStore
}
```

- [ ] **Step 2: Add IsBrokerTool to MCPBroker interface**

In `internal/broker/broker.go`, add to the `MCPBroker` interface:

```go
// IsBrokerTool returns true if the tool name is handled directly by the broker
IsBrokerTool(name string) bool
```

And add the method implementation:

```go
func (m *mcpBrokerImpl) IsBrokerTool(name string) bool {
	return IsBrokerTool(name)
}
```

- [ ] **Step 3: Initialize sessionScopes and register tools in NewBroker**

In `internal/broker/broker.go`, in `NewBroker()`:
- Initialize `sessionScopes: newSessionScopeStore()` in the struct literal
- Add session cleanup in `AddOnUnregisterSession` hook
- Register discovery tools after creating `listeningMCPServer`

After the `listeningMCPServer` creation line, add:

```go
mcpBkr.listeningMCPServer.AddTools(discoveryTools(mcpBkr)...)
```

Update the `AddOnUnregisterSession` hook to clean up scopes:

```go
hooks.AddOnUnregisterSession(func(_ context.Context, session server.ClientSession) {
	slog.Info("Broker: Gateway client session unregister ", "gatewaySessionID", session.SessionID())
	mcpBkr.sessionScopes.removeSession(session.SessionID())
})
```

- [ ] **Step 4: Verify it compiles**

Run: `go build ./...`
Expected: clean compilation

- [ ] **Step 5: Commit**

```bash
git add internal/broker/broker.go
git commit -s -m "register discovery meta-tools and session scope lifecycle"
```

---

### Task 4: Add session scope filter to FilterTools

**Files:**
- Modify: `internal/broker/filtered_tools_handler.go:25-49`
- Create test cases in: `internal/broker/discovery_test.go` (append)

- [ ] **Step 1: Write the failing test**

Add to `internal/broker/discovery_test.go`:

```go
func TestApplySessionScopeFilter(t *testing.T) {
	store := newSessionScopeStore()
	broker := &mcpBrokerImpl{
		sessionScopes: store,
		logger:        slog.Default(),
	}

	tools := []mcp.Tool{
		{Name: "discover_tools"},
		{Name: "select_tools"},
		{Name: "test1_greet"},
		{Name: "test1_time"},
		{Name: "test2_hello"},
	}

	t.Run("no scope returns all tools", func(t *testing.T) {
		result := broker.applySessionScopeFilter("no-scope-session", tools)
		if len(result) != 5 {
			t.Errorf("expected 5 tools, got %d", len(result))
		}
	})

	t.Run("scope filters to selected tools plus meta-tools", func(t *testing.T) {
		store.setScope("scoped-session", []string{"test1_greet", "test1_time"})
		result := broker.applySessionScopeFilter("scoped-session", tools)
		if len(result) != 4 { // 2 selected + 2 meta-tools
			t.Errorf("expected 4 tools, got %d: %v", len(result), toolNames(result))
		}
		names := toolNames(result)
		for _, expected := range []string{"discover_tools", "select_tools", "test1_greet", "test1_time"} {
			if !containsName(names, expected) {
				t.Errorf("expected %q in result, got %v", expected, names)
			}
		}
	})
}

func toolNames(tools []mcp.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway/.claude/worktrees/tool-discovery-proposal && go test ./internal/broker/ -run "TestApplySessionScopeFilter" -v`
Expected: FAIL — `applySessionScopeFilter` not defined

- [ ] **Step 3: Add applySessionScopeFilter to discovery.go**

Add to `internal/broker/discovery.go`:

```go
// applySessionScopeFilter filters tools based on the session's selected scope.
// Meta-tools (discover_tools, select_tools) are always included.
func (broker *mcpBrokerImpl) applySessionScopeFilter(sessionID string, tools []mcp.Tool) []mcp.Tool {
	scope := broker.sessionScopes.getScope(sessionID)
	if scope == nil {
		return tools
	}

	var filtered []mcp.Tool
	for _, tool := range tools {
		if IsBrokerTool(tool.Name) || scope[tool.Name] {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}
```

- [ ] **Step 4: Wire into FilterTools**

In `internal/broker/filtered_tools_handler.go`, modify `FilterTools` to add session scope filtering as step 3. The session ID is available from the request context. Add after the virtual server filter and before `removeGatewayMeta`:

```go
// step 3: apply session scope filtering
session := server.ClientSessionFromContext(ctx)
if session != nil {
	tools = broker.applySessionScopeFilter(session.SessionID(), tools)
}
```

Add the import for `server` package if not already present:

```go
"github.com/mark3labs/mcp-go/server"
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway/.claude/worktrees/tool-discovery-proposal && go test ./internal/broker/ -run "TestApplySessionScopeFilter" -v`
Expected: PASS

- [ ] **Step 6: Run all broker tests**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway/.claude/worktrees/tool-discovery-proposal && go test ./internal/broker/ -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/broker/discovery.go internal/broker/discovery_test.go internal/broker/filtered_tools_handler.go
git commit -s -m "add session scope filter to tools/list pipeline"
```

---

### Task 5: Router awareness of broker tools

**Files:**
- Modify: `internal/mcp-router/request_handlers.go:220-222`

- [ ] **Step 1: Write the test**

The router tests use mock brokers. Add `IsBrokerTool` to the mock. Find the existing mock broker interface used in router tests and ensure it includes `IsBrokerTool(name string) bool`. The test should verify that a `tools/call` for `discover_tools` is handled via `HandleNoneToolCall` (broker passthrough) rather than upstream routing.

Check existing router test patterns in `internal/mcp-router/` for how to write this test. The key assertion: a `tools/call` with tool name `discover_tools` should NOT produce a "tool not found" error.

- [ ] **Step 2: Add IsBrokerTool check in HandleMCPRequest**

In `internal/mcp-router/request_handlers.go`, modify the switch in `HandleMCPRequest` (around line 220):

Change:
```go
case mcpReq.Method == methodToolCall:
	span.SetAttributes(attribute.String("mcp.route", "tool-call"))
	return s.HandleToolCall(ctx, mcpReq)
```

To:
```go
case mcpReq.Method == methodToolCall:
	if s.Broker.IsBrokerTool(mcpReq.ToolName()) {
		span.SetAttributes(attribute.String("mcp.route", "broker-tool"))
		return s.HandleNoneToolCall(ctx, mcpReq)
	}
	span.SetAttributes(attribute.String("mcp.route", "tool-call"))
	return s.HandleToolCall(ctx, mcpReq)
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./...`
Expected: clean compilation

- [ ] **Step 4: Run lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/mcp-router/request_handlers.go
git commit -s -m "route broker meta-tools through broker passthrough path"
```

---

### Task 6: Integration verification

- [ ] **Step 1: Run full unit test suite**

Run: `cd /Users/maleck13/projects/src/github.com/kuadrant/mcp-gateway/.claude/worktrees/tool-discovery-proposal && make test-unit`
Expected: PASS

- [ ] **Step 2: Run lint**

Run: `make lint`
Expected: PASS

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: clean compilation

- [ ] **Step 4: Commit any fixes if needed**

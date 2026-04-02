package broker

import (
	"log/slog"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
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

	t.Run("no scope returns nil and false", func(t *testing.T) {
		tools, exists := store.getScope("session-1")
		if tools != nil {
			t.Errorf("expected nil, got %v", tools)
		}
		if exists {
			t.Error("expected exists=false for unset scope")
		}
	})

	t.Run("set and get scope", func(t *testing.T) {
		store.setScope("session-1", []string{"tool_a", "tool_b"})
		tools, exists := store.getScope("session-1")
		if !exists {
			t.Fatal("expected exists=true")
		}
		if len(tools) != 2 {
			t.Fatalf("expected 2 tools, got %d", len(tools))
		}
		if tools["tool_a"] != true || tools["tool_b"] != true {
			t.Errorf("unexpected scope: %v", tools)
		}
	})

	t.Run("empty list sets scope to show all", func(t *testing.T) {
		store.setScope("session-1", []string{})
		tools, exists := store.getScope("session-1")
		if !exists {
			t.Error("expected exists=true after empty set")
		}
		if len(tools) != 0 {
			t.Errorf("expected empty scope, got %v", tools)
		}
	})

	t.Run("remove session", func(t *testing.T) {
		store.setScope("session-2", []string{"tool_c"})
		store.removeSession("session-2")
		tools, exists := store.getScope("session-2")
		if exists {
			t.Error("expected exists=false after remove")
		}
		if tools != nil {
			t.Errorf("expected nil after remove, got %v", tools)
		}
	})
}

func TestApplySessionScopeFilter(t *testing.T) {
	tools := []mcp.Tool{
		{Name: "discover_tools"},
		{Name: "select_tools"},
		{Name: "test1_greet"},
		{Name: "test1_time"},
		{Name: "test2_hello"},
	}

	t.Run("no scope below threshold shows all tools", func(t *testing.T) {
		store := newSessionScopeStore()
		b := &mcpBrokerImpl{
			sessionScopes:          store,
			logger:                 slog.Default(),
			discoveryToolThreshold: 10, // 3 non-meta tools, under threshold
		}
		result := b.applySessionScopeFilter("no-scope-session", tools)
		if len(result) != 5 {
			t.Errorf("expected 5 tools, got %d: %v", len(result), toolNames(result))
		}
	})

	t.Run("no scope above threshold shows only meta-tools", func(t *testing.T) {
		store := newSessionScopeStore()
		b := &mcpBrokerImpl{
			sessionScopes:          store,
			logger:                 slog.Default(),
			discoveryToolThreshold: 2, // 3 non-meta tools, above threshold
		}
		result := b.applySessionScopeFilter("no-scope-session", tools)
		if len(result) != 2 {
			t.Errorf("expected 2 meta-tools, got %d: %v", len(result), toolNames(result))
		}
		for _, name := range toolNames(result) {
			if !IsBrokerTool(name) {
				t.Errorf("unexpected non-meta tool %q in default hidden result", name)
			}
		}
	})

	t.Run("no scope at exact threshold shows all tools", func(t *testing.T) {
		store := newSessionScopeStore()
		b := &mcpBrokerImpl{
			sessionScopes:          store,
			logger:                 slog.Default(),
			discoveryToolThreshold: 3, // exactly 3 non-meta tools
		}
		result := b.applySessionScopeFilter("no-scope-session", tools)
		if len(result) != 5 {
			t.Errorf("expected 5 tools at threshold, got %d: %v", len(result), toolNames(result))
		}
	})

	t.Run("threshold 0 always hides", func(t *testing.T) {
		store := newSessionScopeStore()
		b := &mcpBrokerImpl{
			sessionScopes:          store,
			logger:                 slog.Default(),
			discoveryToolThreshold: 0,
		}
		result := b.applySessionScopeFilter("no-scope-session", tools)
		if len(result) != 2 {
			t.Errorf("expected 2 meta-tools with threshold 0, got %d: %v", len(result), toolNames(result))
		}
	})

	t.Run("scope filters to selected tools plus meta-tools", func(t *testing.T) {
		store := newSessionScopeStore()
		b := &mcpBrokerImpl{
			sessionScopes:          store,
			logger:                 slog.Default(),
			discoveryToolThreshold: 10,
		}
		store.setScope("scoped-session", []string{"test1_greet", "test1_time"})
		result := b.applySessionScopeFilter("scoped-session", tools)
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

	t.Run("empty scope shows all tools regardless of threshold", func(t *testing.T) {
		store := newSessionScopeStore()
		b := &mcpBrokerImpl{
			sessionScopes:          store,
			logger:                 slog.Default(),
			discoveryToolThreshold: 0,
		}
		store.setScope("reset-session", []string{})
		result := b.applySessionScopeFilter("reset-session", tools)
		if len(result) != 5 {
			t.Errorf("expected 5 tools after reset, got %d: %v", len(result), toolNames(result))
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

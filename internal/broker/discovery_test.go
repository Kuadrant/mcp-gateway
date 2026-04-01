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

func TestApplySessionScopeFilter(t *testing.T) {
	store := newSessionScopeStore()
	b := &mcpBrokerImpl{
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
		result := b.applySessionScopeFilter("no-scope-session", tools)
		if len(result) != 5 {
			t.Errorf("expected 5 tools, got %d", len(result))
		}
	})

	t.Run("scope filters to selected tools plus meta-tools", func(t *testing.T) {
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

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

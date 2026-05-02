package controller

import (
	"strings"
	"testing"
	"time"

	mcpv1alpha1 "github.com/Kuadrant/mcp-gateway/api/v1alpha1"
)

func TestBuildServerTimeouts(t *testing.T) {
	t.Run("nil spec returns nil with no error", func(t *testing.T) {
		got, err := buildServerTimeouts(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil result, got %#v", got)
		}
	})

	t.Run("empty spec returns nil with no error", func(t *testing.T) {
		got, err := buildServerTimeouts(&mcpv1alpha1.MCPServerTimeouts{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != nil {
			t.Fatalf("expected nil result for empty spec, got %#v", got)
		}
	})

	t.Run("default and overrides parse and round-trip", func(t *testing.T) {
		spec := &mcpv1alpha1.MCPServerTimeouts{
			ToolCall: "10s",
			PerTool: []mcpv1alpha1.ToolTimeout{
				{Name: "slow", ToolCall: "1m"},
				{Name: "fast", ToolCall: "250ms"},
			},
		}

		got, err := buildServerTimeouts(spec)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil {
			t.Fatal("expected non-nil result")
		}
		if got.ToolCall != 10*time.Second {
			t.Errorf("default = %v, want 10s", got.ToolCall)
		}
		if got.PerTool["slow"] != time.Minute {
			t.Errorf("perTool[slow] = %v, want 1m", got.PerTool["slow"])
		}
		if got.PerTool["fast"] != 250*time.Millisecond {
			t.Errorf("perTool[fast] = %v, want 250ms", got.PerTool["fast"])
		}
	})

	t.Run("invalid duration returns descriptive error", func(t *testing.T) {
		spec := &mcpv1alpha1.MCPServerTimeouts{ToolCall: "definitely-not-a-duration"}
		_, err := buildServerTimeouts(spec)
		if err == nil {
			t.Fatal("expected error for invalid duration")
		}
		if !strings.Contains(err.Error(), "toolCall") {
			t.Errorf("error should mention the field name, got: %v", err)
		}
	})

	t.Run("non-positive duration is rejected", func(t *testing.T) {
		spec := &mcpv1alpha1.MCPServerTimeouts{ToolCall: "0s"}
		_, err := buildServerTimeouts(spec)
		if err == nil || !strings.Contains(err.Error(), "greater than zero") {
			t.Errorf("expected greater-than-zero validation error, got: %v", err)
		}
	})

	t.Run("negative per-tool duration is rejected", func(t *testing.T) {
		spec := &mcpv1alpha1.MCPServerTimeouts{
			PerTool: []mcpv1alpha1.ToolTimeout{
				{Name: "broken", ToolCall: "-5s"},
			},
		}
		_, err := buildServerTimeouts(spec)
		if err == nil || !strings.Contains(err.Error(), "broken") {
			t.Errorf("expected error mentioning the tool name, got: %v", err)
		}
	})

	t.Run("duplicate per-tool entry is rejected", func(t *testing.T) {
		spec := &mcpv1alpha1.MCPServerTimeouts{
			PerTool: []mcpv1alpha1.ToolTimeout{
				{Name: "twice", ToolCall: "5s"},
				{Name: "twice", ToolCall: "10s"},
			},
		}
		_, err := buildServerTimeouts(spec)
		if err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Errorf("expected duplicate error, got: %v", err)
		}
	})
}

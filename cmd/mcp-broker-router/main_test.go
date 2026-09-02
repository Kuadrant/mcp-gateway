package main

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
)

func TestSetupLoggerLevelMapping(t *testing.T) {
	cases := []struct {
		name  string
		level int
		want  slog.Level
	}{
		{"info", 0, slog.LevelInfo},
		{"warn", 4, slog.LevelWarn},
		{"error", 8, slog.LevelError},
		{"debug", -4, slog.LevelDebug},
		{"arbitrary", 2, slog.Level(2)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &app{}
			a.brokerCfg.logLevel = tc.level
			opts, _ := a.setupLogger()
			if got := opts.Level.Level(); got != tc.want {
				t.Errorf("log-level=%d: got %v, want %v", tc.level, got, tc.want)
			}
		})
	}
}

// TestApplyConfigSnapshot tests the applyConfigSnapshot function.
func TestApplyConfigSnapshot(t *testing.T) {
	a := &app{
		mcpConfig: &config.MCPServersConfig{},
		logger:    slog.New(slog.NewTextHandler(os.Stdout, nil)),
	}

	t.Run("nil global guardrails stores nil checker", func(t *testing.T) {
		if err := a.applyConfigSnapshot(context.Background(), &configSnapshot{}); err != nil {
			t.Fatal(err)
		}
		if a.mcpConfig.GetGuardrailsChecker() != nil {
			t.Fatal("expected nil checker")
		}
	})

	t.Run("global guardrails creates a checker", func(t *testing.T) {
		if err := a.applyConfigSnapshot(context.Background(), &configSnapshot{
			globalGuardrails: &config.GuardrailsConfig{
				URL:   "http://127.0.0.1:1",
				Model: "test-model",
			},
		}); err != nil {
			t.Fatal(err)
		}
		if a.mcpConfig.GetGuardrailsChecker() == nil {
			t.Fatal("expected checker to be created")
		}
	})

	t.Run("clearing global guardrails destroys the checker", func(t *testing.T) {
		if err := a.applyConfigSnapshot(context.Background(), &configSnapshot{}); err != nil {
			t.Fatal(err)
		}
		if a.mcpConfig.GetGuardrailsChecker() != nil {
			t.Fatal("expected nil checker after clearing guardrails")
		}
	})

	t.Run("invalid gateway CA aborts without swapping live config", func(t *testing.T) {
		prevServers := []*config.MCPServer{{Name: "prev"}}
		a.mcpConfig.ApplyReload(prevServers, nil, "prev-ca", 0, nil, nil)

		err := a.applyConfigSnapshot(context.Background(), &configSnapshot{
			servers:          []*config.MCPServer{{Name: "next"}},
			gatewayCACertPEM: "not a certificate",
			globalGuardrails: &config.GuardrailsConfig{
				URL:   "http://127.0.0.1:1",
				Model: "test-model",
			},
		})
		if err == nil {
			t.Fatal("expected error for invalid CA PEM")
		}
		servers := a.mcpConfig.ListServers()
		if len(servers) != 1 || servers[0].Name != "prev" {
			t.Fatalf("live servers = %+v, want previous snapshot", servers)
		}
		if a.mcpConfig.GetGuardrailsChecker() != nil {
			t.Fatal("checker must stay nil when reload aborts")
		}
		if a.mcpConfig.GetGlobalGuardrails() != nil {
			t.Fatal("global guardrails must stay nil when reload aborts")
		}
	})
}

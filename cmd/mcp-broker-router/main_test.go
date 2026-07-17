package main

import (
	"log/slog"
	"testing"
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

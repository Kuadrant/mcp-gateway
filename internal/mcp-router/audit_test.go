package mcprouter

import (
	"strings"
	"testing"
)

func TestExtractToolParams(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		params  map[string]any
		want    string
	}{
		{
			name:    "disabled returns empty",
			enabled: false,
			params:  map[string]any{"name": "greet", "arguments": map[string]any{"msg": "hi"}},
			want:    "",
		},
		{
			name:    "enabled with arguments",
			enabled: true,
			params:  map[string]any{"name": "greet", "arguments": map[string]any{"msg": "hi"}},
			want:    `{"msg":"hi"}`,
		},
		{
			name:    "enabled with no arguments key",
			enabled: true,
			params:  map[string]any{"name": "greet"},
			want:    "",
		},
		{
			name:    "enabled with nil params",
			enabled: true,
			params:  nil,
			want:    "",
		},
		{
			name:    "enabled with empty arguments",
			enabled: true,
			params:  map[string]any{"name": "greet", "arguments": map[string]any{}},
			want:    "{}",
		},
		{
			name:    "enabled with nested arguments",
			enabled: true,
			params:  map[string]any{"name": "create", "arguments": map[string]any{"title": "bug", "labels": []string{"p0", "critical"}}},
			want:    `{"labels":["p0","critical"],"title":"bug"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractToolParams(tt.enabled, tt.params)
			if got != tt.want {
				t.Errorf("extractToolParams() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractToolParamsTruncation(t *testing.T) {
	largeValue := strings.Repeat("x", 2048)
	params := map[string]any{
		"name":      "greet",
		"arguments": map[string]any{"data": largeValue},
	}
	got := extractToolParams(true, params)
	if got != "[truncated]" {
		t.Errorf("extractToolParams() = %q, want %q", got, "[truncated]")
	}
}

func TestTruncateID(t *testing.T) {
	short := "jane@example.com"
	if got := truncateID(short); got != short {
		t.Errorf("truncateID(%q) = %q, want unchanged", short, got)
	}
	long := strings.Repeat("a", 300)
	got := truncateID(long)
	if len(got) != maxIDBytes {
		t.Errorf("truncateID() len = %d, want %d", len(got), maxIDBytes)
	}
}

package mcprouter

import (
	"os"
	"strings"
	"testing"
)

func TestExtractToolParams(t *testing.T) {
	tests := []struct {
		name      string
		envVal    string
		req       *MCPRequest
		want      string
		wantLen   int // if truncation is expected
	}{
		{
			name:   "disabled by default",
			envVal: "",
			req: &MCPRequest{
				Method: "tools/call",
				Params: map[string]any{
					"name": "greet",
					"arguments": map[string]any{
						"msg": "hello",
					},
				},
			},
			want: "",
		},
		{
			name:   "disabled explicitly",
			envVal: "false",
			req: &MCPRequest{
				Method: "tools/call",
				Params: map[string]any{
					"name": "greet",
					"arguments": map[string]any{
						"msg": "hello",
					},
				},
			},
			want: "",
		},
		{
			name:   "enabled but not tool/call",
			envVal: "true",
			req: &MCPRequest{
				Method: "tools/list",
				Params: map[string]any{
					"arguments": map[string]any{
						"msg": "hello",
					},
				},
			},
			want: "",
		},
		{
			name:   "enabled and arguments present",
			envVal: "true",
			req: &MCPRequest{
				Method: "tools/call",
				Params: map[string]any{
					"name": "greet",
					"arguments": map[string]any{
						"msg": "hello",
					},
				},
			},
			want: `{"msg":"hello"}`,
		},
		{
			name:   "enabled but arguments missing",
			envVal: "true",
			req: &MCPRequest{
				Method: "tools/call",
				Params: map[string]any{
					"name": "greet",
				},
			},
			want: "",
		},
		{
			name:   "enabled and arguments exceed 1KB",
			envVal: "true",
			req: &MCPRequest{
				Method: "tools/call",
				Params: map[string]any{
					"name": "long_args",
					"arguments": map[string]any{
						"data": strings.Repeat("a", 1500),
					},
				},
			},
			wantLen: 1024,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envVal != "" {
				os.Setenv("MCP_AUDIT_LOG_PARAMS", tt.envVal)
				defer os.Unsetenv("MCP_AUDIT_LOG_PARAMS")
			} else {
				os.Unsetenv("MCP_AUDIT_LOG_PARAMS")
			}

			got := ExtractToolParams(tt.req)
			if tt.wantLen > 0 {
				if len(got) != tt.wantLen {
					t.Errorf("ExtractToolParams() returned length = %v, want %v", len(got), tt.wantLen)
				}
			} else {
				if got != tt.want {
					t.Errorf("ExtractToolParams() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

package mcprouter

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
)

func TestTimeoutSourceFor(t *testing.T) {
	tests := []struct {
		name     string
		timeouts *config.ServerTimeouts
		tool     string
		want     string
	}{
		{
			name:     "nil timeouts has no source",
			timeouts: nil,
			tool:     "x",
			want:     "",
		},
		{
			name:     "empty timeouts has no source",
			timeouts: &config.ServerTimeouts{},
			tool:     "x",
			want:     "",
		},
		{
			name:     "default only",
			timeouts: &config.ServerTimeouts{ToolCall: 5 * time.Second},
			tool:     "x",
			want:     "server",
		},
		{
			name: "perTool match wins",
			timeouts: &config.ServerTimeouts{
				ToolCall: 5 * time.Second,
				PerTool:  map[string]time.Duration{"x": 30 * time.Second},
			},
			tool: "x",
			want: "perTool",
		},
		{
			name: "perTool with zero is ignored",
			timeouts: &config.ServerTimeouts{
				ToolCall: 5 * time.Second,
				PerTool:  map[string]time.Duration{"x": 0},
			},
			tool: "x",
			want: "server",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := timeoutSourceFor(tt.timeouts, tt.tool); got != tt.want {
				t.Errorf("timeoutSourceFor = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildToolTimeoutSSEEvent(t *testing.T) {
	raw := buildToolTimeoutSSEEvent(7, "slow_tool", 1234)

	if !strings.HasPrefix(string(raw), "event: message\ndata: ") {
		t.Fatalf("missing SSE framing in: %s", raw)
	}
	if !strings.HasSuffix(string(raw), "\n\n") {
		t.Fatalf("missing SSE terminator in: %s", raw)
	}

	// Pull the JSON payload out of the SSE frame and validate it.
	const dataPrefix = "data: "
	bodyLine := string(raw)
	bodyLine = strings.TrimPrefix(bodyLine, "event: message\n")
	bodyLine = strings.TrimPrefix(bodyLine, dataPrefix)
	bodyLine = strings.TrimSuffix(bodyLine, "\n\n")

	var parsed struct {
		JSONRPC string `json:"jsonrpc"`
		ID      any    `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Data    struct {
				TimeoutMS int64  `json:"timeoutMs"`
				Tool      string `json:"tool"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(bodyLine), &parsed); err != nil {
		t.Fatalf("failed to decode payload %q: %v", bodyLine, err)
	}

	if parsed.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", parsed.JSONRPC)
	}
	if parsed.Error.Code != jsonRPCToolTimeoutCode {
		t.Errorf("error.code = %d, want %d", parsed.Error.Code, jsonRPCToolTimeoutCode)
	}
	if parsed.Error.Data.TimeoutMS != 1234 {
		t.Errorf("error.data.timeoutMs = %d, want 1234", parsed.Error.Data.TimeoutMS)
	}
	if parsed.Error.Data.Tool != "slow_tool" {
		t.Errorf("error.data.tool = %q, want slow_tool", parsed.Error.Data.Tool)
	}
	if !strings.Contains(parsed.Error.Message, "slow_tool") {
		t.Errorf("message should reference the tool name, got %q", parsed.Error.Message)
	}
	if !strings.Contains(parsed.Error.Message, "1234ms") {
		t.Errorf("message should reference the timeout, got %q", parsed.Error.Message)
	}

	// JSON-RPC permits ID values of any JSON type. json.Unmarshal decodes numbers
	// into float64 by default; both forms are valid as long as the round-trip is lossless.
	switch v := parsed.ID.(type) {
	case float64:
		if v != 7 {
			t.Errorf("id = %v, want 7", v)
		}
	default:
		t.Errorf("id has unexpected type %T", v)
	}
}

func TestWithUpstreamRequestTimeoutMSHeader(t *testing.T) {
	headers := NewHeaders().WithUpstreamRequestTimeoutMS(2500).Build()

	var found bool
	for _, h := range headers {
		if h.Header == nil {
			continue
		}
		if h.Header.Key == upstreamTimeoutHeader {
			found = true
			if string(h.Header.RawValue) != "2500" {
				t.Errorf("header value = %q, want 2500", string(h.Header.RawValue))
			}
		}
	}
	if !found {
		t.Fatalf("expected header %q in %#v", upstreamTimeoutHeader, headers)
	}
}

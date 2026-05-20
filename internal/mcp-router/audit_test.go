package mcprouter

import (
	"strings"
	"testing"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
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

func TestBuildAuditMetadata(t *testing.T) {
	getMetadataField := func(t *testing.T, srv *ExtProcServer, req *MCPRequest, field string) string {
		t.Helper()
		md := srv.buildAuditMetadata(req)
		if md == nil {
			t.Fatal("expected non-nil metadata")
		}
		auditNS := md.Fields[auditMetadataNS]
		if auditNS == nil {
			t.Fatal("expected mcp.audit namespace in metadata")
		}
		val := auditNS.GetStructValue().Fields[field]
		if val == nil {
			t.Fatalf("expected field %q in metadata", field)
		}
		return val.GetStringValue()
	}

	t.Run("nil audit config returns nil", func(t *testing.T) {
		srv := &ExtProcServer{Audit: nil}
		req := &MCPRequest{}
		md := srv.buildAuditMetadata(req)
		if md != nil {
			t.Errorf("expected nil metadata, got %v", md)
		}
	})

	t.Run("baggage user and agent populate metadata", func(t *testing.T) {
		srv := &ExtProcServer{Audit: &AuditConfig{}}
		req := &MCPRequest{
			Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{
				{Key: "baggage", RawValue: []byte("user.id=jane,agent.id=bot-v2")},
			}},
		}
		if got := getMetadataField(t, srv, req, metadataUserID); got != "jane" {
			t.Errorf("user_id = %q, want %q", got, "jane")
		}
		if got := getMetadataField(t, srv, req, metadataAgentID); got != "bot-v2" {
			t.Errorf("agent_id = %q, want %q", got, "bot-v2")
		}
	})

	t.Run("no baggage sets dash values", func(t *testing.T) {
		srv := &ExtProcServer{Audit: &AuditConfig{}}
		req := &MCPRequest{
			Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{}},
		}
		if got := getMetadataField(t, srv, req, metadataUserID); got != "-" {
			t.Errorf("user_id = %q, want %q", got, "-")
		}
		if got := getMetadataField(t, srv, req, metadataAgentID); got != "-" {
			t.Errorf("agent_id = %q, want %q", got, "-")
		}
	})

	t.Run("params enabled populates tool_params", func(t *testing.T) {
		srv := &ExtProcServer{Audit: &AuditConfig{ParameterLogging: "Enabled"}}
		req := &MCPRequest{
			Params:  map[string]any{"arguments": map[string]any{"msg": "hi"}},
			Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{}},
		}
		if got := getMetadataField(t, srv, req, metadataToolParam); got != `{"msg":"hi"}` {
			t.Errorf("tool_params = %q, want %q", got, `{"msg":"hi"}`)
		}
	})

	t.Run("params disabled sets dash", func(t *testing.T) {
		srv := &ExtProcServer{Audit: &AuditConfig{ParameterLogging: "Disabled"}}
		req := &MCPRequest{
			Params:  map[string]any{"arguments": map[string]any{"msg": "hi"}},
			Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{}},
		}
		if got := getMetadataField(t, srv, req, metadataToolParam); got != "-" {
			t.Errorf("tool_params = %q, want %q", got, "-")
		}
	})

	t.Run("identity headers fallback when no baggage", func(t *testing.T) {
		srv := &ExtProcServer{Audit: &AuditConfig{IdentityHeaders: []string{"x-custom-user"}}}
		req := &MCPRequest{
			Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{
				{Key: "x-custom-user", RawValue: []byte("custom-jane")},
			}},
		}
		if got := getMetadataField(t, srv, req, metadataUserID); got != "custom-jane" {
			t.Errorf("user_id = %q, want %q", got, "custom-jane")
		}
	})

	t.Run("empty identity headers means baggage only", func(t *testing.T) {
		srv := &ExtProcServer{Audit: &AuditConfig{IdentityHeaders: nil}}
		req := &MCPRequest{
			Headers: &corev3.HeaderMap{Headers: []*corev3.HeaderValue{
				{Key: "x-forwarded-email", RawValue: []byte("should-be-ignored")},
			}},
		}
		if got := getMetadataField(t, srv, req, metadataUserID); got != "-" {
			t.Errorf("user_id = %q, want %q (should not fall back to x-forwarded-email)", got, "-")
		}
	})
}

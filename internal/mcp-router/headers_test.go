package mcprouter

import (
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/routing"
)

func Test_Headers(t *testing.T) {
	headersBuilder := NewHeaders()

	expected := map[string]string{
		routing.ToolHeader:            "test_tool",
		routing.ToolAnnotationsHeader: "destructive=true,idempotent=true,readOnly=false",
		routing.AuthorityHeader:       "mcp1.mcp.local",
		"authorization":               "auth",
		routing.MethodHeader:          "tools/call",
		routing.MCPServerNameHeader:   "mcp1",
		routing.SessionHeader:         "xxxx",
		":path":                       "/mcp1",
	}

	headers := headersBuilder.
		WithAuthority(expected[routing.AuthorityHeader]).
		WithAuth(expected["authorization"]).
		WithMCPMethod(expected[routing.MethodHeader]).
		WithMCPServerName(expected[routing.MCPServerNameHeader]).
		WithMCPSession(expected[routing.SessionHeader]).
		WithMCPToolName(expected[routing.ToolHeader]).
		WithToolAnnotations(expected[routing.ToolAnnotationsHeader]).
		WithPath("/mcp1").
		Build()

	for key, value := range expected {
		found := false
		for _, h := range headers {
			if h.Header.Key == key && string(h.Header.RawValue) == value {
				found = true
				continue
			}
		}
		if !found {
			t.Fatalf("did not find %s in %v", key, headers)
		}
	}
}

package mcprouter

import (
	"testing"
)

func Test_Headers(t *testing.T) {
	headersBuilder := NewHeaders()

	expected := map[string]string{
		toolHeader:            "test_tool",
		toolAnnotationsHeader: "destructive=true,idempotent=true,readOnly=false",
		toolReadOnlyHeader:    "false",
		toolDestructiveHeader: "true",
		toolIdempotentHeader:  "true",
		toolOpenWorldHeader:   "false",
		authorityHeader:       "mcp1.mcp.local",
		"authorization":       "auth",
		methodHeader:          "tools/call",
		mcpServerNameHeader:   "mcp1",
		sessionHeader:         "xxxx",
		":path":               "/mcp1",
	}

	headers := headersBuilder.
		WithAuthority(expected[authorityHeader]).
		WithAuth(expected["authorization"]).
		WithMCPMethod(expected[methodHeader]).
		WithMCPServerName(expected[mcpServerNameHeader]).
		WithMCPSession(expected[sessionHeader]).
		WithMCPToolName(expected[toolHeader]).
		WithToolAnnotations(expected[toolAnnotationsHeader]).
		WithToolAnnotationHint(toolReadOnlyHeader, expected[toolReadOnlyHeader]).
		WithToolAnnotationHint(toolDestructiveHeader, expected[toolDestructiveHeader]).
		WithToolAnnotationHint(toolIdempotentHeader, expected[toolIdempotentHeader]).
		WithToolAnnotationHint(toolOpenWorldHeader, expected[toolOpenWorldHeader]).
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

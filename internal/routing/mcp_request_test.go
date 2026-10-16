package routing

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPRequest_RewritesPreserveParams(t *testing.T) {
	for _, tc := range []struct {
		method  string
		field   string
		rewrite func(*MCPRequest, string)
	}{
		{MethodToolCall, "name", (*MCPRequest).ReWriteToolName},
		{MethodPromptGet, "name", (*MCPRequest).ReWritePromptName},
		{MethodResourceRead, "uri", (*MCPRequest).ReWriteResourceURI},
	} {
		t.Run(tc.method, func(t *testing.T) {
			payload := `{"jsonrpc":"2.0","id":1,"method":"` + tc.method + `","params":{"` + tc.field + `":"prefixed","arguments":{"nested":[9007199254740993,true,null,{"text":"value"}]},"capabilities":{"custom":{"id":9007199254740993}},"_meta":{"progressToken":9007199254740993},"extension":[9007199254740993,{"enabled":false}]}}`
			var req MCPRequest
			require.NoError(t, json.Unmarshal([]byte(payload), &req))
			tc.rewrite(&req, "original")
			body, err := req.ToBytes()
			require.NoError(t, err)
			var before, after struct {
				Params map[string]json.RawMessage `json:"params"`
			}
			require.NoError(t, json.Unmarshal([]byte(payload), &before))
			require.NoError(t, json.Unmarshal(body, &after))
			before.Params[tc.field] = json.RawMessage(`"original"`)
			require.Equal(t, before.Params, after.Params)
		})
	}
}

func TestMCPRequest_MissingParams(t *testing.T) {
	for _, params := range []string{"", `,"params":null`} {
		var req MCPRequest
		require.NoError(t, json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call"`+params+`}`), &req))
		require.Nil(t, req.Params)
		require.Empty(t, req.ToolName())
		req.Method = MethodPromptGet
		require.Empty(t, req.PromptName())
		req.Method = MethodResourceRead
		require.Empty(t, req.ResourceURI())
		req.Method = MethodInitialize
		require.False(t, req.ClientSupportsElicitation())
		body, err := req.ToBytes()
		require.NoError(t, err)
		require.NotContains(t, string(body), `"params"`)

		req.ReWriteToolName("")
		req.ReWritePromptName("prompt")
		req.ReWriteResourceURI("")
		body, err = req.ToBytes()
		require.NoError(t, err)
		require.Contains(t, string(body), `"name":"prompt"`)
		require.Contains(t, string(body), `"uri":""`)
	}
}

func TestMCPRequest_EmptyParams(t *testing.T) {
	var req MCPRequest
	require.NoError(t, json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`), &req))
	require.NotNil(t, req.Params)
	require.Empty(t, req.ToolName())
	body, err := req.ToBytes()
	require.NoError(t, err)
	require.NotContains(t, string(body), `"params"`)
	req.ReWriteToolName("")
	body, err = req.ToBytes()
	require.NoError(t, err)
	require.Contains(t, string(body), `"name":""`)
}

func TestMCPRequest_NonStringParams(t *testing.T) {
	for _, value := range []string{`null`, `42`, `false`, `{}`, `[]`, `""`} {
		var req MCPRequest
		payload := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":` + value + `,"uri":` + value + `}}`
		require.NoError(t, json.Unmarshal([]byte(payload), &req))
		require.Empty(t, req.ToolName())
		req.Method = MethodPromptGet
		require.Empty(t, req.PromptName())
		req.Method = MethodResourceRead
		require.Empty(t, req.ResourceURI())
		req.Method = MethodToolCall
		body, err := req.ToBytes()
		require.NoError(t, err)
		require.JSONEq(t, payload, string(body))
	}
}

func TestMCPRequest_ClientSupportsElicitation(t *testing.T) {
	for _, tc := range []struct {
		capabilities string
		want         bool
	}{
		{`null`, false},
		{`{}`, false},
		{`[]`, false},
		{`"elicitation"`, false},
		{`42`, false},
		{`{"roots":{}}`, false},
		{`{"elicitation":{}}`, true},
		{`{"elicitation":null}`, true},
		{`{"elicitation":{"form":{},"url":{}}}`, true},
	} {
		t.Run(tc.capabilities, func(t *testing.T) {
			var req MCPRequest
			require.NoError(t, json.Unmarshal([]byte(`{"method":"initialize","params":{"capabilities":`+tc.capabilities+`}}`), &req))
			require.Equal(t, tc.want, req.ClientSupportsElicitation())
			req.Method = MethodToolCall
			require.False(t, req.ClientSupportsElicitation())
		})
	}
	var req MCPRequest
	require.NoError(t, json.Unmarshal([]byte(`{"method":"initialize","params":{}}`), &req))
	require.False(t, req.ClientSupportsElicitation())
}

func TestInjectResourcePrefix(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		prefix  string
		wantURI string
		wantOK  bool
	}{
		{"basic prefix injection", "ui://template.html", "insights", "ui://insights_template.html", true},
		{"prefix already has separator", "ui://template.html", "insights_", "ui://insights_template.html", true},
		{"non-ui scheme untouched", "https://example.com/x.html", "insights", "https://example.com/x.html", false},
		{"malformed uri untouched", "ui://\x7fbad", "insights", "ui://\x7fbad", false},
		{"empty prefix untouched host", "ui://template.html", "", "ui://template.html", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := InjectResourcePrefix(tt.uri, tt.prefix)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.wantURI {
				t.Errorf("got %q, want %q", got, tt.wantURI)
			}
		})
	}
}

func TestStripResourcePrefix(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		prefix  string
		wantURI string
	}{
		{"basic prefix stripped", "ui://insights_template.html", "insights", "ui://template.html"},
		{"prefix already has separator", "ui://insights_template.html", "insights_", "ui://template.html"},
		{"non-ui scheme untouched", "https://example.com/x.html", "insights", "https://example.com/x.html"},
		{"malformed uri untouched", "ui://\x7fbad", "insights", "ui://\x7fbad"},
		{"host without prefix untouched", "ui://template.html", "insights", "ui://template.html"},
		{"empty prefix untouched", "ui://template.html", "", "ui://template.html"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripResourcePrefix(tt.uri, tt.prefix)
			if got != tt.wantURI {
				t.Errorf("got %q, want %q", got, tt.wantURI)
			}
		})
	}
}

func TestInjectThenStripResourcePrefix_RoundTrips(t *testing.T) {
	uri := "ui://template.html"
	prefix := "insights"

	injected, ok := InjectResourcePrefix(uri, prefix)
	if !ok {
		t.Fatalf("InjectResourcePrefix(%q, %q) returned ok=false", uri, prefix)
	}
	if got := StripResourcePrefix(injected, prefix); got != uri {
		t.Errorf("StripResourcePrefix(%q, %q) = %q, want %q", injected, prefix, got, uri)
	}
}

func TestStripAuthorityPrefix(t *testing.T) {
	tests := []struct {
		name          string
		authority     string
		prefix        string
		wantAuthority string
	}{
		{"basic prefix stripped", "app_example.com", "app", "example.com"},
		{"prefix already has separator", "app_example.com", "app_", "example.com"},
		{"authority without matching prefix untouched", "example.com", "app", "example.com"},
		{"empty prefix untouched", "example.com", "", "example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripAuthorityPrefix(tt.authority, tt.prefix)
			if got != tt.wantAuthority {
				t.Errorf("got %q, want %q", got, tt.wantAuthority)
			}
		})
	}
}

package broker

// TODO: remove with http_compat.go when we adopt native SDK protocol behaviour.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/broker/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// mark3labs answered POSTs with buffered application/json, never SSE.
func TestCompat_POSTResponsesAreJSON(t *testing.T) {
	h := newCompatHarness(t)
	sid := h.initialize(t)

	res := h.post(t, sid, h.toolsListBody())
	require.Equal(t, http.StatusOK, res.status, res.body)
	require.Equal(t, "application/json", res.header.Get("Content-Type"))
	require.Contains(t, res.body, `"tools"`)
	require.NotContains(t, res.body, "event:", "response must not be SSE framed")
	// mark3labs framed JSON via json.Encoder and set no cache headers
	require.True(t, strings.HasSuffix(res.body, "\n"), "body must end with newline: %q", res.body)
	require.Empty(t, res.header.Get("Cache-Control"))
}

// requests the SDK transport would reject with HTTP 400 got mark3labs
// JSON-RPC error bodies at 200, and unsupported capabilities -32601s.
func TestCompat_MainDispatchTable(t *testing.T) {
	h := newCompatHarness(t)
	sid := h.initialize(t)

	cases := []struct {
		name     string
		body     string
		status   int
		ct       string
		exact    string
		contains string
	}{
		{
			name:   "unknown method",
			body:   `{"jsonrpc":"2.0","id":1,"method":"foo/bar","params":{}}`,
			status: http.StatusOK, ct: "application/json",
			exact: `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method foo/bar not found"}}` + "\n",
		},
		{
			name:   "server discover probe",
			body:   `{"jsonrpc":"2.0","id":2,"method":"server/discover","params":{}}`,
			status: http.StatusOK, ct: "application/json",
			exact: `{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"Method server/discover not found"}}` + "\n",
		},
		{
			name:   "resources list",
			body:   `{"jsonrpc":"2.0","id":3,"method":"resources/list","params":{}}`,
			status: http.StatusOK, ct: "application/json",
			exact: `{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"resources not supported"}}` + "\n",
		},
		{
			name:   "resources templates list",
			body:   `{"jsonrpc":"2.0","id":4,"method":"resources/templates/list","params":{}}`,
			status: http.StatusOK, ct: "application/json",
			exact: `{"jsonrpc":"2.0","id":4,"error":{"code":-32601,"message":"resources not supported"}}` + "\n",
		},
		{
			name:   "resources read",
			body:   `{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"file:///x"}}`,
			status: http.StatusOK, ct: "application/json",
			exact: `{"jsonrpc":"2.0","id":5,"error":{"code":-32601,"message":"resources not supported"}}` + "\n",
		},
		{
			name:   "resources subscribe",
			body:   `{"jsonrpc":"2.0","id":6,"method":"resources/subscribe","params":{"uri":"file:///x"}}`,
			status: http.StatusOK, ct: "application/json",
			exact: `{"jsonrpc":"2.0","id":6,"error":{"code":-32601,"message":"resources not supported"}}` + "\n",
		},
		{
			name:   "logging setLevel",
			body:   `{"jsonrpc":"2.0","id":7,"method":"logging/setLevel","params":{"level":"debug"}}`,
			status: http.StatusOK, ct: "application/json",
			exact: `{"jsonrpc":"2.0","id":7,"error":{"code":-32601,"message":"logging not supported"}}` + "\n",
		},
		{
			name:   "completion complete",
			body:   `{"jsonrpc":"2.0","id":8,"method":"completion/complete","params":{}}`,
			status: http.StatusOK, ct: "application/json",
			exact: `{"jsonrpc":"2.0","id":8,"error":{"code":-32601,"message":"completions not supported"}}` + "\n",
		},
		{
			name:   "tasks get",
			body:   `{"jsonrpc":"2.0","id":9,"method":"tasks/get","params":{}}`,
			status: http.StatusOK, ct: "application/json",
			exact: `{"jsonrpc":"2.0","id":9,"error":{"code":-32601,"message":"tasks not supported"}}` + "\n",
		},
		{
			name:   "string id echoed",
			body:   `{"jsonrpc":"2.0","id":"abc","method":"nope","params":{}}`,
			status: http.StatusOK, ct: "application/json",
			exact: `{"jsonrpc":"2.0","id":"abc","error":{"code":-32601,"message":"Method nope not found"}}` + "\n",
		},
		{
			name:   "bad jsonrpc version",
			body:   `{"jsonrpc":"1.0","id":1,"method":"ping"}`,
			status: http.StatusOK, ct: "application/json",
			exact: `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid JSON-RPC version"}}` + "\n",
		},
		{
			name:   "malformed json",
			body:   `{"jsonrpc":`,
			status: http.StatusBadRequest, ct: "application/json",
			exact: `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"request body is not valid json"}}` + "\n",
		},
		{
			name:   "batch rejected as parse error",
			body:   `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","id":2,"method":"ping"}]`,
			status: http.StatusBadRequest, ct: "application/json",
			exact: `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"request body is not valid json"}}` + "\n",
		},
		{
			name:   "unknown notification accepted",
			body:   `{"jsonrpc":"2.0","method":"notifications/whatever"}`,
			status: http.StatusAccepted,
		},
		{
			name:   "request method without id treated as notification",
			body:   `{"jsonrpc":"2.0","method":"tools/list","params":{}}`,
			status: http.StatusAccepted,
		},
		{
			name:   "notification with array params",
			body:   `{"jsonrpc":"2.0","method":"notifications/whatever","params":[1]}`,
			status: http.StatusOK, ct: "application/json",
			exact: `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"Failed to parse notification"}}` + "\n",
		},
		{
			name:   "ping response body accepted",
			body:   `{"jsonrpc":"2.0","id":5,"result":{}}`,
			status: http.StatusAccepted,
		},
		{
			name:   "stray sampling response",
			body:   `{"jsonrpc":"2.0","id":5,"result":{"x":1}}`,
			status: http.StatusBadRequest, ct: "text/plain; charset=utf-8",
			exact: "No pending sampling request found for the given request ID\n",
		},
		{
			name:   "ping delegated",
			body:   `{"jsonrpc":"2.0","id":11,"method":"ping"}`,
			status: http.StatusOK, ct: "application/json",
			contains: `"result"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := h.post(t, sid, tc.body)
			require.Equal(t, tc.status, res.status, res.body)
			if tc.ct != "" {
				require.Equal(t, tc.ct, res.header.Get("Content-Type"))
			}
			if tc.exact != "" {
				require.Equal(t, tc.exact, res.body)
			}
			if tc.contains != "" {
				require.Contains(t, res.body, tc.contains)
			}
			if tc.status == http.StatusAccepted {
				require.Empty(t, res.body)
			}
		})
	}
}

// mark3labs rejected bad content types with 400 text, not the SDK's 415.
func TestCompat_ContentType400(t *testing.T) {
	h := newCompatHarness(t)
	sid := h.initialize(t)
	res := h.do(t, http.MethodPost, sid, h.toolsListBody(), map[string]string{"Content-Type": "text/plain"})
	require.Equal(t, http.StatusBadRequest, res.status)
	require.Equal(t, "Invalid content type: must be 'application/json'\n", res.body)
	require.Equal(t, "text/plain; charset=utf-8", res.header.Get("Content-Type"))
}

// envoy bounds bodies before they reach the broker in production; direct
// access must not allow unbounded allocation. bodies within the bound are
// unaffected.
func TestCompat_OversizedBodyRejected(t *testing.T) {
	h := newCompatHarness(t)
	sid := h.initialize(t)

	oversized := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"pad":"` +
		strings.Repeat("a", maxRequestBodyBytes+1) + `"}}`
	res := h.post(t, sid, oversized)
	require.Equal(t, http.StatusRequestEntityTooLarge, res.status, res.body)
	require.Equal(t, "application/json", res.header.Get("Content-Type"))
	require.Equal(t, `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"request body too large"}}`+"\n", res.body)

	// a large-but-bounded body still dispatches normally
	padded := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/list","params":{"_meta":{"pad":%q}}}`,
		h.rpcID.Add(1), strings.Repeat("a", 1<<10))
	res = h.post(t, sid, padded)
	require.Equal(t, http.StatusOK, res.status, res.body)
	require.Contains(t, res.body, `"tools"`)
}

// mark3labs never required an Accept header.
func TestCompat_AcceptHeaderOptional(t *testing.T) {
	h := newCompatHarness(t)
	sid := h.initialize(t)
	res := h.do(t, http.MethodPost, sid, h.toolsListBody(), map[string]string{"Accept": ""})
	require.Equal(t, http.StatusOK, res.status, res.body)
	res = h.do(t, http.MethodPost, sid, h.toolsListBody(), map[string]string{"Accept": "application/json"})
	require.Equal(t, http.StatusOK, res.status, res.body)
}

// mark3labs ignored the MCP-Protocol-Version header entirely.
func TestCompat_ProtocolVersionHeaderIgnored(t *testing.T) {
	h := newCompatHarness(t)
	sid := h.initialize(t)
	res := h.do(t, http.MethodPost, sid, h.toolsListBody(), map[string]string{"Mcp-Protocol-Version": "not-a-version"})
	require.Equal(t, http.StatusOK, res.status, res.body)
}

// invalid and expired sessions must 404 immediately with mark3labs' text
// bodies, before any transport or JSON-RPC machinery runs.
func TestCompat_SessionValidationPreTransport(t *testing.T) {
	h := newCompatHarness(t)
	h.initialize(t)

	cases := []struct {
		name string
		sid  string
		msg  string
	}{
		{"garbage token", "not-a-jwt", "Invalid session ID\n"},
		{"terminated token", "terminated-123", "Session terminated\n"},
		{"missing session header", "", "Invalid session ID\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := h.post(t, tc.sid, h.toolsListBody())
			require.Equal(t, http.StatusNotFound, res.status)
			require.Equal(t, tc.msg, res.body)
			require.Equal(t, "text/plain; charset=utf-8", res.header.Get("Content-Type"))
		})
	}
}

// a POST carrying an empty Mcp-Session-Id header value must 404 exactly
// like an absent one: checkSession sees the empty string either way.
func TestCompat_EmptySessionHeader404(t *testing.T) {
	h := newCompatHarness(t)
	h.initialize(t)

	// raw request: the harness helper drops empty headers, and an
	// empty-valued header is a distinct wire shape from an absent one
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, h.ts.URL, strings.NewReader(h.toolsListBody()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", "")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "Invalid session ID\n", string(body))
	require.Equal(t, "text/plain; charset=utf-8", resp.Header.Get("Content-Type"))
}

// a request on a live session whose JWT has gone invalid must 404 and tear
// the SDK session down so its table entry and GET stream are released.
func TestCompat_InvalidSessionTornDown(t *testing.T) {
	h := newCompatHarness(t)
	sid := h.initialize(t)
	require.Equal(t, 1, h.serverSessionCount(sid))

	// same session id no longer validates (prefix no longer matches)
	res := h.do(t, http.MethodPost, sid, h.toolsListBody(), map[string]string{"Mcp-Session-Id": "x" + sid})
	require.Equal(t, http.StatusNotFound, res.status)

	// the original session is untouched; now invalidate it for real by
	// making the validator refuse everything via a terminated-prefix id
	res = h.post(t, "terminated-"+sid, h.toolsListBody())
	require.Equal(t, http.StatusNotFound, res.status)
	require.Equal(t, "Session terminated\n", res.body)

	// direct hit on the real session id with the validator flipped is not
	// possible with the prefix validator, so drop it via DELETE below
	del := h.do(t, http.MethodDelete, sid, "", nil)
	require.Equal(t, http.StatusOK, del.status)
	require.Eventually(t, func() bool { return h.serverSessionCount(sid) == 0 }, 5*time.Second, 20*time.Millisecond)
}

// mark3labs' DELETE always answered 200 after asking the session manager to
// terminate, whether or not the session was known to this pod.
func TestCompat_DeleteAlways200(t *testing.T) {
	h := newCompatHarness(t)
	sid := h.initialize(t)

	for _, target := range []string{sid, "valid-unknown-pod", "not-a-jwt", ""} {
		res := h.do(t, http.MethodDelete, target, "", nil)
		require.Equal(t, http.StatusOK, res.status, "session %q", target)
		require.Empty(t, res.body)
	}
	_, ok := h.terminated.Load(sid)
	require.True(t, ok, "terminator must run for DELETE")
}

// re-initialize with an existing session header gets a fresh session, as
// mark3labs always generated a new ID for initialize.
func TestCompat_ReinitializeCreatesFreshSession(t *testing.T) {
	h := newCompatHarness(t)
	sid := h.initialize(t)

	res := h.do(t, http.MethodPost, sid, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`, nil)
	require.Equal(t, http.StatusOK, res.status, res.body)
	require.NotContains(t, res.body, `"error"`)
	fresh := res.header.Get("Mcp-Session-Id")
	require.NotEmpty(t, fresh)
	require.NotEqual(t, sid, fresh)

	// both sessions remain usable
	for _, s := range []string{sid, fresh} {
		list := h.post(t, s, h.toolsListBody())
		require.Equal(t, http.StatusOK, list.status, list.body)
	}
}

// mark3labs negotiated 2025-03-26 when the client sent an empty protocol
// version (or no params at all); the SDK would answer 2025-11-25.
func TestCompat_EmptyProtocolVersionNegotiates20250326(t *testing.T) {
	h := newCompatHarness(t)

	for name, body := range map[string]string{
		"empty version": `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`,
		"no version":    `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"c","version":"1"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			res := h.post(t, "", body)
			require.Equal(t, http.StatusOK, res.status, res.body)
			require.Contains(t, res.body, `"protocolVersion":"2025-03-26"`)
		})
	}
	t.Run("explicit version honoured", func(t *testing.T) {
		res := h.post(t, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`)
		require.Equal(t, http.StatusOK, res.status, res.body)
		require.Contains(t, res.body, `"protocolVersion":"2025-06-18"`)
	})
}

// a GET with a session mark3labs would have streamed for must not 400/404:
// invalid sessions get an equivalent hanging SSE stream.
func TestCompat_GETInvalidSessionStreams(t *testing.T) {
	h := newCompatHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.ts.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Mcp-Session-Id", "not-a-jwt")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	require.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
	// nothing arrives; the read must block until our context deadline
	buf := make([]byte, 1)
	_, readErr := resp.Body.Read(buf)
	require.Error(t, readErr)
}

// non-GET/POST/DELETE methods fell through to 404 in mark3labs, not 405.
func TestCompat_UnknownVerb404(t *testing.T) {
	h := newCompatHarness(t)
	res := h.do(t, http.MethodPut, "", `{}`, nil)
	require.Equal(t, http.StatusNotFound, res.status)
}

// tools/list result bodies carried no ttlMs/cacheScope on mark3labs, and
// every tool had an annotations key holding the upstream's exact bytes ({}
// when a tool declared none). the SDK adds cache fields and re-marshals
// annotations with explicit false bools.
func TestCompat_ToolsListResultRewrite(t *testing.T) {
	h := newCompatHarness(t)

	mcpServer, manager := createTestManagerMCP(t, "up1", "up_", []mcp.Tool{
		{Name: "annotated"},
		{Name: "plain"},
	})
	mcpServer.SetToolHintsForTesting(map[string]upstream.ToolHints{
		"up_annotated": {Raw: json.RawMessage(`{"readOnlyHint":true,"destructiveHint":false}`)},
	})
	h.b.mcpServers["up1"] = upstream.NewActiveForTesting(manager)

	// gateway-registered tools as the SDK client decode produced them: the
	// annotated one has collapsed bools, the plain one none at all
	h.b.gatewayServer.AddTools(
		upstream.GatewayTool{
			Tool: mcp.Tool{
				Name:        "up_annotated",
				InputSchema: map[string]any{"type": "object"},
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			},
			Handler: func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			},
		},
		upstream.GatewayTool{
			Tool: mcp.Tool{
				Name:        "up_plain",
				InputSchema: map[string]any{"type": "object"},
			},
			Handler: func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			},
		},
	)

	sid := h.initialize(t)
	res := h.post(t, sid, h.toolsListBody())
	require.Equal(t, http.StatusOK, res.status, res.body)

	require.NotContains(t, res.body, `"ttlMs"`)
	require.NotContains(t, res.body, `"cacheScope"`)
	require.True(t, strings.HasSuffix(res.body, "\n"))

	var envelope struct {
		Result struct {
			Tools []struct {
				Name        string          `json:"name"`
				Annotations json.RawMessage `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.body), &envelope))
	byName := map[string]json.RawMessage{}
	for _, tool := range envelope.Result.Tools {
		byName[tool.Name] = tool.Annotations
	}
	require.Equal(t, `{"readOnlyHint":true,"destructiveHint":false}`, string(byName["up_annotated"]),
		"annotations must be the raw upstream bytes, not the SDK re-marshal")
	require.Equal(t, `{}`, string(byName["up_plain"]),
		"tools without annotations carried an empty annotations object on mark3labs")
}

// prompts/list results also carried no ttlMs/cacheScope on mark3labs.
func TestCompat_PromptsListResultRewrite(t *testing.T) {
	h := newCompatHarness(t)
	sid := h.initialize(t)
	res := h.post(t, sid, `{"jsonrpc":"2.0","id":42,"method":"prompts/list","params":{}}`)
	require.Equal(t, http.StatusOK, res.status, res.body)
	require.NotContains(t, res.body, `"ttlMs"`)
	require.NotContains(t, res.body, `"cacheScope"`)
	require.Contains(t, res.body, `"prompts"`)
}

// the standalone GET stream must carry mark3labs' exact bytes: no priming
// comment, notifications without the SDK's empty params object, and the
// plain no-cache header.
func TestCompat_GETStreamMainFraming(t *testing.T) {
	h := newCompatHarness(t)
	sid := h.initialize(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.ts.URL, nil)
	require.NoError(t, err)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", sid)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))

	// force a broadcast tools/list_changed
	h.b.gatewayServer.TriggerToolsListChanged("")

	// one full event must arrive; the priming ": ok" comment must not
	buf := make([]byte, 4096)
	var got []byte
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
			if strings.Contains(string(got), "\n\n") && strings.Contains(string(got), "data:") {
				break
			}
		}
		if readErr != nil {
			break
		}
	}
	require.NotContains(t, string(got), ": ok", "SDK priming comment must be stripped")
	require.Equal(t,
		"event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n",
		string(got),
		"notification frame must match mark3labs byte-for-byte")
}

// an SDK client must still receive list_changed through the full compat
// chain (filter + resurrection + JSONResponse).
func TestCompat_NotificationDeliveredToSDKClient(t *testing.T) {
	h := newCompatHarness(t)

	notified := make(chan struct{}, 4)
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0.0.1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			notified <- struct{}{}
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: h.ts.URL}, nil)
	require.NoError(t, err)
	defer func() { _ = cs.Close() }()

	h.b.gatewayServer.TriggerToolsListChanged("")

	select {
	case <-notified:
	case <-time.After(10 * time.Second):
		t.Fatal("tools/list_changed never reached the SDK client through the compat chain")
	}
}

// mark3labs never paginated tools/list; a single request must return every
// tool with no nextCursor even past the SDK's default page size of 1000.
func TestToolsListUnpaginated(t *testing.T) {
	b := NewBroker(logger, WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)

	const total = mcp.DefaultPageSize + 1
	tools := make([]upstream.GatewayTool, 0, total)
	for i := range total {
		tools = append(tools, upstream.GatewayTool{
			Tool: mcp.Tool{
				Name:        fmt.Sprintf("tool-%04d", i),
				InputSchema: map[string]any{"type": "object"},
			},
			Handler: func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			},
		})
	}
	b.gatewayServer.AddTools(tools...)

	ct, st := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	go func() {
		ss, err := b.MCPServer().Connect(ctx, st, nil)
		if err != nil {
			return
		}
		_ = ss.Wait()
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0.0.1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	require.NoError(t, err)
	defer func() { _ = cs.Close() }()

	res, err := cs.ListTools(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, res.NextCursor, "list must not paginate")
	require.Len(t, res.Tools, total)
}

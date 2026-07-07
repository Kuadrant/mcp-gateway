package server2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestHello(t *testing.T) {
	res, err := helloHandler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{},
	})
	require.NoError(t, err)
	require.True(t, res.IsError)

	argsJSON, _ := json.Marshal(map[string]any{"name": "Fred"})
	res, err = helloHandler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Arguments: argsJSON,
		},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.IsType(t, &mcp.TextContent{}, res.Content[0])
	require.Equal(t, "Hello, Fred!", res.Content[0].(*mcp.TextContent).Text)

	// mark3labs RequireString accepted the empty string
	argsJSON, _ = json.Marshal(map[string]any{"name": ""})
	res, err = helloHandler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Arguments: argsJSON,
		},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Equal(t, "Hello, !", res.Content[0].(*mcp.TextContent).Text)
}

func TestTime(t *testing.T) {
	res, err := timeHandler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.IsType(t, &mcp.TextContent{}, res.Content[0])
}

func TestHeaders(t *testing.T) {
	res, err := headersToolHandler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Len(t, res.Content, 0)

	res, err = headersToolHandler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{},
		Extra: &mcp.RequestExtra{
			Header: http.Header{
				"Authorization": []string{"bearer 1234"},
			},
		},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	require.IsType(t, &mcp.TextContent{}, res.Content[0])
	require.Equal(t, "Authorization: [bearer 1234]", res.Content[0].(*mcp.TextContent).Text)
}

func TestSlow(t *testing.T) {
	res, err := slowHandler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{},
	})
	require.NoError(t, err)
	require.True(t, res.IsError)

	// seconds is passed as string "0" matching MCP inspector convention
	argsJSON, _ := json.Marshal(map[string]any{"seconds": "0"})
	res, err = slowHandler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Arguments: argsJSON,
		},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.IsType(t, &mcp.TextContent{}, res.Content[0])
}

func TestAuth1234(t *testing.T) {
	_, err := auth1234ToolHandler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{},
	})
	require.Error(t, err)

	res, err := auth1234ToolHandler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{},
		Extra: &mcp.RequestExtra{
			Header: http.Header{
				"Authorization": []string{"bearer 1234"},
			},
		},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.IsType(t, &mcp.TextContent{}, res.Content[0])
}

func TestRunStreamableServer(t *testing.T) {
	startFunc, shutdownFunc, err := RunServer("http", "8085")
	require.NoError(t, err)
	require.NotNil(t, startFunc)
	require.NotNil(t, shutdownFunc)
}

// the SDK's streamable handler flushes SSE headers via http.ResponseController.
// logResponse must not swallow Flush, or standalone SSE GET responses never
// leave the server's buffer and clients hang waiting for headers.
func TestLogResponsePreservesFlush(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(": ok\n\n"))
		require.NoError(t, http.NewResponseController(w).Flush())
		<-r.Context().Done() // hang like a standalone SSE stream
	})

	srv := httptest.NewServer(logResponse(inner))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	require.NoError(t, err)

	// must receive response headers while the handler is still hanging
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "response headers should arrive before the handler returns")
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
}

// mark3labs' RequireInt accepted numbers as well as numeric strings for
// the slow tool's seconds argument.
func TestSlowAcceptsNumericSeconds(t *testing.T) {
	argsJSON, _ := json.Marshal(map[string]any{"seconds": 0})
	res, err := slowHandler(context.Background(), &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{
			Arguments: argsJSON,
		},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "numeric seconds must be accepted as mark3labs did")
}

// the slow tool must send notifications/progress while it works when the
// caller provided a progress token, as it did on mark3labs.
func TestSlowSendsProgressNotifications(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "progress-probe", Version: "1.0.0"}, nil)
	slow := testTools["slow"]
	s.AddTool(&slow.tool, slow.handler)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	ts := httptest.NewServer(handler)
	defer ts.Close()

	progress := make(chan *mcp.ProgressNotificationParams, 8)
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			progress <- req.Params
		},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL}, nil)
	require.NoError(t, err)
	defer func() { _ = cs.Close() }()

	params := &mcp.CallToolParams{Name: "slow", Arguments: map[string]any{"seconds": "2"}}
	params.SetProgressToken("tok-1")
	res, err := cs.CallTool(ctx, params)
	require.NoError(t, err)
	require.False(t, res.IsError)

	select {
	case p := <-progress:
		require.Equal(t, "tok-1", p.ProgressToken)
		require.Contains(t, p.Message, "Waited")
	default:
		t.Fatal("no progress notification received during slow call")
	}
}

// /admin/forget must genuinely forget the session again: subsequent
// requests carrying the forgotten ID are 404s, as with mark3labs'
// UnregisterSession.
func TestAdminForgetDropsSession(t *testing.T) {
	s := mcp.NewServer(&mcp.Implementation{Name: "forget-probe", Version: "1.0.0"}, nil)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil)
	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.HandleFunc("/admin/forget", forgetFuncFactory(s))
	ts := httptest.NewServer(mux)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	require.NoError(t, err)
	defer func() { _ = cs.Close() }()
	sid := cs.ID()
	require.NotEmpty(t, sid)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/admin/forget", strings.NewReader(sid))
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// a raw request with the forgotten session must 404
	body := `{"jsonrpc":"2.0","id":9,"method":"tools/list","params":{}}`
	raw, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/mcp", strings.NewReader(body))
	require.NoError(t, err)
	raw.Header.Set("Content-Type", "application/json")
	raw.Header.Set("Accept", "application/json, text/event-stream")
	raw.Header.Set("Mcp-Session-Id", sid)
	rawResp, err := http.DefaultClient.Do(raw)
	require.NoError(t, err)
	defer func() { _ = rawResp.Body.Close() }()
	require.Equal(t, http.StatusNotFound, rawResp.StatusCode, "forgotten session must be gone")
}

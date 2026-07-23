// dual-protocol demo: connects to the same gateway as both a 2025 and 2026
// client, showing that each sees only protocol-compatible tools.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	green  = "\033[32m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
	red    = "\033[31m"
)

func main() {
	gatewayURL := os.Getenv("GATEWAY_URL")
	if gatewayURL == "" {
		gatewayURL = "http://mcp.127-0-0-1.sslip.io:8001/mcp"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	banner("MCP Gateway Dual Protocol Demo")
	fmt.Printf("  %sgateway:%s %s\n\n", dim, reset, gatewayURL)

	statefulURL := strings.TrimSuffix(gatewayURL, "/mcp") + "/mcp/stateful"

	steps := []struct {
		title string
		fn    func()
	}{
		{
			"Step 1: Connect as 2026-07-28 (stateless) client to /mcp",
			func() {
				explain("The SDK sends server/discover to negotiate the protocol version.")
				explain("The gateway responds with supportedVersions based on registered backends.")
				run2026Client(ctx, gatewayURL)
			},
		},
		{
			"Step 2: Connect as 2025-11-25 (stateful) client to /mcp",
			func() {
				explain("server/discover is blocked via custom code in the client to force the SDK to fall back to initialize and 2025 protocol.")
				explain("The gateway negotiates 2025-11-25 via the legacy handshake.")
				run2025Client(ctx, gatewayURL)
			},
		},
		{
			"Step 3: Connect to /mcp/stateful (2026 client accessing 2025 tools)",
			func() {
				explain("The /mcp/stateful route forces 2025-11-25 negotiation.")
				explain("A 2026-capable agent uses this to access tools from 2025-only backends.")
				run2025Client(ctx, statefulURL)
			},
		},
	}

	for _, step := range steps {
		section(step.title)
		step.fn()
		fmt.Println()
	}

	banner("Demo Complete")
}

func run2026Client(ctx context.Context, url string) {
	client := mcp.NewClient(&mcp.Implementation{Name: "demo-2026", Version: "1.0"}, nil)
	t := &mcp.StreamableClientTransport{
		Endpoint:             url,
		HTTPClient:           &http.Client{Transport: &loggingTransport{base: http.DefaultTransport}},
		DisableStandaloneSSE: true,
	}

	session, err := client.Connect(ctx, t, nil)
	if err != nil {
		resultf("connect error: %v", err)
		return
	}
	defer func() { _ = session.Close() }()

	resultf("negotiated: %s%s%s", green, session.InitializeResult().ProtocolVersion, reset)
	listAndCallTool(ctx, session, "stateless")
}

func run2025Client(ctx context.Context, url string) {
	client := mcp.NewClient(&mcp.Implementation{Name: "demo-2025", Version: "1.0"}, nil)
	t := &mcp.StreamableClientTransport{
		Endpoint: url,
		HTTPClient: &http.Client{Transport: &loggingTransport{
			base: &block2026Transport{base: http.DefaultTransport},
		}},
		DisableStandaloneSSE: true,
	}

	session, err := client.Connect(ctx, t, nil)
	if err != nil {
		resultf("connect error: %v", err)
		return
	}
	defer func() { _ = session.Close() }()

	resultf("negotiated: %s%s%s", green, session.InitializeResult().ProtocolVersion, reset)
	listAndCallTool(ctx, session, "stateful")
}

func listAndCallTool(ctx context.Context, session *mcp.ClientSession, label string) {
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		resultf("tools/list error: %v", err)
		return
	}

	resultf("tools/list returned %s%d tools%s:", bold, len(tools.Tools), reset)
	for _, t := range tools.Tools {
		fmt.Printf("       - %s\n", t.Name)
	}

	if len(tools.Tools) == 0 {
		return
	}

	calls := map[string]map[string]any{
		"everything_echo":       {"message": "hello from " + label + " client"},
		"stateless_hello_world": {"name": label},
	}
	for toolName, args := range calls {
		for _, t := range tools.Tools {
			if t.Name != toolName {
				continue
			}
			callResult, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name:      toolName,
				Arguments: args,
			})
			if err != nil {
				resultf("tools/call %s %serror%s: %v", toolName, red, reset, err)
			} else if len(callResult.Content) > 0 {
				if tc, ok := callResult.Content[0].(*mcp.TextContent); ok {
					resultf("tools/call %s %s%s%s", toolName, green, tc.Text, reset)
				}
			}
			break
		}
	}
}

// --- output helpers ---

func banner(title string) {
	line := strings.Repeat("=", len(title)+4)
	fmt.Printf("\n%s%s%s\n", bold, line, reset)
	fmt.Printf("%s  %s  %s\n", bold, title, reset)
	fmt.Printf("%s%s%s\n\n", bold, line, reset)
}

func section(title string) {
	fmt.Printf("%s%s%s\n", bold+cyan, title, reset)
}

func explain(msg string) {
	fmt.Printf("  %s%s%s\n", dim, msg, reset)
}

func resultf(format string, args ...any) {
	fmt.Printf("  %s> %s%s\n", yellow, reset, fmt.Sprintf(format, args...))
}

func wiref(direction, format string, args ...any) {
	fmt.Printf("  %s%s%s %s\n", dim, direction, reset, fmt.Sprintf(format, args...))
}

// --- transport wrappers ---

type loggingTransport struct {
	base http.RoundTripper
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, method := peekMethod(req)
	if method != "server/discover" && method != "initialize" {
		return t.base.RoundTrip(req)
	}

	pv := req.Header.Get("Mcp-Protocol-Version")
	if pv == "" {
		pv = "(none)"
	}
	wiref(">>", "%s  protocol-version: %s", method, pv)
	if body != nil {
		prettyPrint("     request", body)
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		wiref("<<", "%serror: %v%s", red, err, reset)
		return resp, err
	}

	respBody, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	color := green
	if resp.StatusCode >= 400 {
		color = red
	}
	wiref("<<", "%s%d%s", color, resp.StatusCode, reset)
	prettyPrint("     response", respBody)
	return resp, nil
}

func prettyPrint(label string, data []byte) {
	var obj map[string]any
	if json.Unmarshal(data, &obj) != nil {
		fmt.Printf("  %s%s: %s%s\n", dim, label, truncate(string(data), 200), reset)
		return
	}
	pretty, err := json.MarshalIndent(obj, "  "+strings.Repeat(" ", len(label)), "  ")
	if err != nil {
		fmt.Printf("  %s%s: %s%s\n", dim, label, truncate(string(data), 200), reset)
		return
	}
	fmt.Printf("  %s%s: %s%s\n", dim, label, string(pretty), reset)
}

func peekMethod(req *http.Request) ([]byte, string) {
	if req.Body == nil {
		return nil, ""
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, ""
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	var env struct {
		Method string `json:"method"`
	}
	_ = json.Unmarshal(body, &env)
	return body, env.Method
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

type block2026Transport struct {
	base http.RoundTripper
}

func (t *block2026Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Del("Mcp-Protocol-Version")
	return t.base.RoundTrip(req)
}

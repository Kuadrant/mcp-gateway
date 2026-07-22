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

func main() {
	gatewayURL := os.Getenv("GATEWAY_URL")
	if gatewayURL == "" {
		gatewayURL = "http://mcp.127-0-0-1.sslip.io:8001/mcp"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("=== MCP Gateway Dual Protocol Demo ===")
	fmt.Printf("gateway: %s\n\n", gatewayURL)

	// 1. connect as a 2026 (stateless) client via /mcp
	fmt.Println("--- 2026-07-28 client (stateless) via /mcp ---")
	run2026Client(ctx, gatewayURL)

	// 2. connect as a 2025 (stateful) client via /mcp
	fmt.Println("\n--- 2025-11-25 client (stateful) via /mcp ---")
	run2025Client(ctx, gatewayURL)

	// 3. connect via protocol-specific routes
	statefulURL := strings.TrimSuffix(gatewayURL, "/mcp") + "/mcp/stateful"
	statelessURL := strings.TrimSuffix(gatewayURL, "/mcp") + "/mcp/stateless"

	fmt.Println("\n--- /mcp/stateful route (forces 2025) ---")
	run2025Client(ctx, statefulURL)

	fmt.Println("\n--- /mcp/stateless route (forces 2026) ---")
	run2026Client(ctx, statelessURL)
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
		fmt.Printf("  connect error: %v\n", err)
		return
	}
	defer func() { _ = session.Close() }()

	fmt.Printf("  negotiated: %s\n", session.InitializeResult().ProtocolVersion)
	listAndCallTool(ctx, session, "stateless")
}

func run2025Client(ctx context.Context, url string) {
	client := mcp.NewClient(&mcp.Implementation{Name: "demo-2025", Version: "1.0"}, nil)
	t := &mcp.StreamableClientTransport{
		Endpoint: url,
		HTTPClient: &http.Client{Transport: &loggingTransport{
			base:      &block2026Transport{base: http.DefaultTransport},
			blockNote: " [server/discover blocked]",
		}},
		DisableStandaloneSSE: true,
	}

	session, err := client.Connect(ctx, t, nil)
	if err != nil {
		fmt.Printf("  connect error: %v\n", err)
		return
	}
	defer func() { _ = session.Close() }()

	fmt.Printf("  negotiated: %s\n", session.InitializeResult().ProtocolVersion)
	listAndCallTool(ctx, session, "stateful")
}

func listAndCallTool(ctx context.Context, session *mcp.ClientSession, label string) {
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		fmt.Printf("  tools/list error: %v\n", err)
		return
	}

	fmt.Printf("  tools (%d):\n", len(tools.Tools))
	for _, t := range tools.Tools {
		fmt.Printf("    - %s\n", t.Name)
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
			result, err := session.CallTool(ctx, &mcp.CallToolParams{
				Name:      toolName,
				Arguments: args,
			})
			if err != nil {
				fmt.Printf("  tools/call %s error: %v\n", toolName, err)
			} else if len(result.Content) > 0 {
				if tc, ok := result.Content[0].(*mcp.TextContent); ok {
					fmt.Printf("  called %s → %s\n", toolName, tc.Text)
				}
			}
			break
		}
	}
}

// loggingTransport logs negotiation requests (server/discover, initialize)
// with their responses.
type loggingTransport struct {
	base      http.RoundTripper
	blockNote string
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, method := peekMethod(req)
	if method != "server/discover" && method != "initialize" {
		return t.base.RoundTrip(req)
	}

	pv := req.Header.Get("Mcp-Protocol-Version")
	fmt.Printf("  >> %s %s  Mcp-Protocol-Version: %s%s\n", req.Method, req.URL, pv, t.blockNote)
	if body != nil {
		fmt.Printf("     request: %s\n", truncate(string(body), 200))
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		fmt.Printf("  << error: %v\n", err)
		return resp, err
	}

	respBody, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(respBody))

	fmt.Printf("  << %d  %s\n", resp.StatusCode, truncate(string(respBody), 300))
	return resp, nil
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

// block2026Transport strips MCP-Protocol-Version header to force 2025 negotiation.
type block2026Transport struct {
	base http.RoundTripper
}

func (t *block2026Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Del("Mcp-Protocol-Version")
	return t.base.RoundTrip(req)
}

// Package userspecificserver implements a test MCP server that returns
// different tools based on the Authorization header. Used for testing
// the userSpecificList feature of the broker.
package userspecificserver

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// StartupFunc is used for functions that will start a server and block until it is finished
type StartupFunc func() error

// ShutdownFunc is used for functions that stop running servers
type ShutdownFunc func() error

const (
	userAToken = "bearer user-a-token"
	userBToken = "bearer user-b-token" //nolint:gosec // test credentials
)

// tool sets keyed by normalized bearer token
var userTools = map[string][]string{
	userAToken: {"list_repos", "create_issue"},
	userBToken: {"run_pipeline"},
}

type testTool struct {
	tool    mcp.Tool
	handler mcp.ToolHandler
}

var allTools = map[string]testTool{
	"server_info": {
		tool: mcp.Tool{
			Name:        "server_info",
			Description: "Returns server identity and detected user",
			InputSchema: map[string]any{"type": "object"},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		handler: serverInfoHandler,
	},
	"headers": {
		tool: mcp.Tool{
			Name:        "headers",
			Description: "Returns all HTTP headers received by the server",
			InputSchema: map[string]any{"type": "object"},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		handler: headersHandler,
	},
	"list_repos": {
		tool: mcp.Tool{
			Name:        "list_repos",
			Description: "List repositories for the authenticated user",
			InputSchema: map[string]any{"type": "object"},
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		},
		handler: stubHandler("list_repos"),
	},
	"create_issue": {
		tool: mcp.Tool{
			Name:        "create_issue",
			Description: "Create an issue in a repository",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title": map[string]any{"type": "string", "description": "Issue title"},
					"repo":  map[string]any{"type": "string", "description": "Repository name"},
				},
				"required": []string{"title", "repo"},
			},
		},
		handler: stubHandler("create_issue"),
	},
	"run_pipeline": {
		tool: mcp.Tool{
			Name:        "run_pipeline",
			Description: "Trigger a CI/CD pipeline run",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
				},
				"required": []string{"pipeline"},
			},
		},
		handler: stubHandler("run_pipeline"),
	},
}

// RunServer creates and returns a startable/stoppable user-specific MCP server
func RunServer(port string) (StartupFunc, ShutdownFunc, error) {
	s := mcp.NewServer(&mcp.Implementation{Name: "user-specific-test-server", Version: "1.0.0"}, &mcp.ServerOptions{})

	for _, tt := range allTools {
		s.AddTool(&tt.tool, tt.handler)
	}

	// filter tools/list responses based on the caller's auth header
	s.AddReceivingMiddleware(toolFilterMiddleware())

	if port == "" {
		port = "9090"
	}

	mux := http.NewServeMux()
	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
	}

	handler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{
		DisableLocalhostProtection: true,
	})
	mux.Handle("/mcp", handler)

	return func() error {
			fmt.Printf("Serving user-specific-server on http://localhost:%s/mcp\n", port)
			return httpServer.ListenAndServe()
		}, func() error {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()
			return httpServer.Shutdown(shutdownCtx)
		}, nil
}

// toolFilterMiddleware filters tools/list results by the caller's auth
// header. always filters: nil Extra/headers means an anonymous caller,
// which must see the base tool set, not everything (fail closed).
func toolFilterMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if err != nil || method != "tools/list" {
				return result, err
			}
			toolsResult, ok := result.(*mcp.ListToolsResult)
			if !ok || toolsResult == nil {
				return result, nil
			}
			headers := http.Header{}
			if extra := req.GetExtra(); extra != nil && extra.Header != nil {
				headers = extra.Header
			}
			filterToolsByAuth(headers, toolsResult)
			return result, nil
		}
	}
}

// filterToolsByAuth removes tools from the result that the user is not
// authorized to see based on the Authorization header.
func filterToolsByAuth(headers http.Header, result *mcp.ListToolsResult) {
	auth := strings.ToLower(headers.Get("Authorization"))

	allowed := map[string]bool{"server_info": true, "headers": true}
	if extras, ok := userTools[auth]; ok {
		for _, name := range extras {
			allowed[name] = true
		}
	}

	filtered := make([]*mcp.Tool, 0, len(allowed))
	for _, tool := range result.Tools {
		if allowed[tool.Name] {
			filtered = append(filtered, tool)
		}
	}
	result.Tools = filtered
}

func serverInfoHandler(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var auth string
	if req.Extra != nil {
		auth = req.Extra.Header.Get("Authorization")
	}
	user := "anonymous"
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		user = auth[7:]
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("server=user-specific-test-server, user=%s", user)}}}, nil
}

func headersHandler(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var lines []string
	if req.Extra != nil {
		for k, v := range req.Extra.Header {
			lines = append(lines, fmt.Sprintf("%s: %s", k, strings.Join(v, ", ")))
		}
	}
	sort.Strings(lines)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Join(lines, "\n")}}}, nil
}

func stubHandler(name string) mcp.ToolHandler {
	return func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%s: ok", name)}}}, nil
	}
}

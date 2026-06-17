// Package userspecificserver implements a test MCP server that returns
// different tools based on the Authorization header. Used for testing
// the userSpecificList feature of the broker.
package userspecificserver

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
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

var allTools = map[string]server.ServerTool{
	"server_info": {
		Tool: mcp.NewTool("server_info",
			mcp.WithDescription("Returns server identity and detected user"),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		Handler: serverInfoHandler,
	},
	"headers": {
		Tool: mcp.NewTool("headers",
			mcp.WithDescription("Returns all HTTP headers received by the server"),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		Handler: headersHandler,
	},
	"list_repos": {
		Tool: mcp.NewTool("list_repos",
			mcp.WithDescription("List repositories for the authenticated user"),
			mcp.WithReadOnlyHintAnnotation(true),
		),
		Handler: stubHandler("list_repos"),
	},
	"create_issue": {
		Tool: mcp.NewTool("create_issue",
			mcp.WithDescription("Create an issue in a repository"),
			mcp.WithString("title", mcp.Required(), mcp.Description("Issue title")),
			mcp.WithString("repo", mcp.Required(), mcp.Description("Repository name")),
		),
		Handler: stubHandler("create_issue"),
	},
	"run_pipeline": {
		Tool: mcp.NewTool("run_pipeline",
			mcp.WithDescription("Trigger a CI/CD pipeline run"),
			mcp.WithString("pipeline", mcp.Required(), mcp.Description("Pipeline name")),
		),
		Handler: stubHandler("run_pipeline"),
	},
}

// RunServer creates and returns a startable/stoppable user-specific MCP server
func RunServer(port string) (StartupFunc, ShutdownFunc, error) {
	hooks := &server.Hooks{}

	hooks.AddOnRegisterSession(func(_ context.Context, session server.ClientSession) {
		log.Printf("Client %s connected", session.SessionID())
	})
	hooks.AddOnUnregisterSession(func(_ context.Context, session server.ClientSession) {
		log.Printf("Client %s disconnected", session.SessionID())
	})
	hooks.AddBeforeAny(func(_ context.Context, _ any, method mcp.MCPMethod, _ any) {
		log.Printf("Processing %s request", method)
	})
	hooks.AddOnError(func(_ context.Context, _ any, method mcp.MCPMethod, _ any, err error) {
		log.Printf("Error in %s: %v", method, err)
	})

	hooks.AddAfterListTools(func(_ context.Context, _ any, req *mcp.ListToolsRequest, result *mcp.ListToolsResult) {
		filterToolsByAuth(req.Header, result)
	})

	s := server.NewMCPServer(
		"user-specific-test-server",
		"1.0.0",
		server.WithHooks(hooks),
		server.WithToolCapabilities(true),
	)

	for _, tool := range allTools {
		s.AddTools(tool)
	}

	if port == "" {
		port = "9090"
	}

	mux := http.NewServeMux()
	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
	}

	streamableHTTPServer := server.NewStreamableHTTPServer(
		s,
		server.WithStreamableHTTPServer(httpServer),
	)
	mux.Handle("/mcp", streamableHTTPServer)

	return func() error {
			fmt.Printf("Serving user-specific-server on http://localhost:%s/mcp\n", port)
			return streamableHTTPServer.Start(":" + port)
		}, func() error {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
			defer cancel()
			return streamableHTTPServer.Shutdown(shutdownCtx)
		}, nil
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

	filtered := make([]mcp.Tool, 0, len(allowed))
	for _, tool := range result.Tools {
		if allowed[tool.Name] {
			filtered = append(filtered, tool)
		}
	}
	result.Tools = filtered
}

func serverInfoHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	auth := req.Header.Get("Authorization")
	user := "anonymous"
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		user = auth[7:]
	}
	return mcp.NewToolResultText(fmt.Sprintf("server=user-specific-test-server, user=%s", user)), nil
}

func headersHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var lines []string
	for k, v := range req.Header {
		lines = append(lines, fmt.Sprintf("%s: %s", k, strings.Join(v, ", ")))
	}
	sort.Strings(lines)
	return mcp.NewToolResultText(strings.Join(lines, "\n")), nil
}

func stubHandler(name string) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(fmt.Sprintf("%s: ok", name)), nil
	}
}

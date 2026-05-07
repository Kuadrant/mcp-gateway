// perf-mock-server: zero-latency MCP server for gateway overhead measurement.
// 10 tools returning instant static responses. isolates gateway overhead from backend noise.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var addr = flag.String("addr", ":8080", "HTTP listen address")

type toolArgs struct {
	Input string `json:"input"`
}

func makeTool(name string) func(context.Context, *mcp.CallToolRequest, toolArgs) (*mcp.CallToolResult, any, error) {
	return func(_ context.Context, _ *mcp.CallToolRequest, params toolArgs) (*mcp.CallToolResult, any, error) {
		input := params.Input
		if input == "" {
			input = "default"
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: fmt.Sprintf("%s: ok [input=%s]", name, input)},
			},
		}, nil, nil
	}
}

var toolNames = []string{
	"alpha", "bravo", "charlie", "delta", "echo",
	"foxtrot", "golf", "hotel", "india", "juliet",
}

func main() {
	flag.Parse()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "perf-mock-server",
		Version: "1.0.0",
	}, nil)

	for _, name := range toolNames {
		mcp.AddTool(server, &mcp.Tool{
			Name:        name,
			Description: fmt.Sprintf("mock tool %s (zero latency)", name),
		}, makeTool(name))
	}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, nil)

	port := *addr
	if envPort := os.Getenv("PORT"); envPort != "" {
		port = ":" + envPort
	}

	log.Printf("perf-mock-server listening on %s (%d tools, 0ms latency)", port, len(toolNames))
	srv := &http.Server{
		Addr:              port,
		Handler:           handler,
		ReadHeaderTimeout: 3 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

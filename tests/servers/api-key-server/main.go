// api-key-test-server is an MCP test server that validates API key authentication
// It validates Authorization headers for all requests when EXPECTED_AUTH is set
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// tool argument struct
type helloArgs struct {
	Name string `json:"name" jsonschema:"the name to greet"`
}

// tool implementation
func helloWorldTool(
	_ context.Context,
	_ *mcp.CallToolRequest,
	params helloArgs,
) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf("Hello, %s! (from authenticated server)", params.Name)},
		},
	}, nil, nil
}

// http middleware for auth validation
type authMiddleware struct {
	Handler      http.Handler
	ExpectedAuth string
}

func (a authMiddleware) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Header.Get("X-Force-Auth-Reject") == "true" {
		log.Printf("Auth force-rejected via X-Force-Auth-Reject header")
		http.Error(w, `{"error": "Unauthorized: token revoked"}`, http.StatusUnauthorized)
		return
	}
	if a.ExpectedAuth != "" {
		auth := req.Header.Get("Authorization")
		if auth != a.ExpectedAuth {
			log.Printf("Auth failed: expected %q, got %q", a.ExpectedAuth, auth)
			http.Error(w,
				fmt.Sprintf(`{"error": "Unauthorized: expected %q, got %q"}`, a.ExpectedAuth, auth),
				http.StatusUnauthorized)
			return
		}
	}
	a.Handler.ServeHTTP(w, req)
}

func main() {
	// get config from environment
	expectedAuth := os.Getenv("EXPECTED_AUTH")
	port := os.Getenv("PORT")
	if port == "" {
		port = "9090"
	}

	if expectedAuth != "" {
		log.Printf("API key test server: Auth required - expecting: %q", expectedAuth)
	} else {
		log.Printf("API key test server: No auth required (EXPECTED_AUTH not set)")
	}

	// create MCP server
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-api-key-test-server"}, nil)

	// register tool
	mcp.AddTool(server, &mcp.Tool{
		Name:        "hello_world",
		Description: "A simple hello world tool that requires authentication",
	}, helloWorldTool)

	// create handler
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return server
	}, nil)

	// wrap with auth middleware
	authHandler := authMiddleware{
		Handler:      handler,
		ExpectedAuth: expectedAuth,
	}

	// setup http server
	mux := http.NewServeMux()
	mux.Handle("/mcp", authHandler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	log.Printf("API key test server listening on :%s", port)
	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
	}

	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

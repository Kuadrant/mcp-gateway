// Package statelessserver implements a minimal MCP server for testing the 2026-07-28 stateless protocol.
// It provides hello_world and headers tools, plus a greeting prompt.
package statelessserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// StartupFunc is used for functions that will start a server and block until it is finished
type StartupFunc func() error

// ShutdownFunc is used for functions that stop running servers
type ShutdownFunc func() error

// RunServer creates a stateless MCP server that can be started and stopped
func RunServer(transport, port string) (StartupFunc, ShutdownFunc, error) {
	s := mcp.NewServer(&mcp.Implementation{Name: "Stateless Test Server", Version: "1.0.0"}, &mcp.ServerOptions{})

	// hello_world tool
	s.AddTool(&mcp.Tool{
		Name:        "hello_world",
		Description: "Say hello to someone",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Name of the person to greet",
				},
			},
			"required": []string{"name"},
		},
	}, helloHandler)

	// headers tool
	s.AddTool(&mcp.Tool{
		Name:        "headers",
		Description: "get HTTP headers",
		InputSchema: map[string]any{
			"type": "object",
		},
	}, headersToolHandler)

	// greeting prompt
	s.AddPrompt(&mcp.Prompt{
		Name:        "greeting",
		Description: "Generate a greeting message",
		Arguments:   []*mcp.PromptArgument{{Name: "name", Required: true, Description: "Name of the person to greet"}},
	}, greetingHandler)

	if port == "" {
		port = "8080"
	}

	switch transport {
	case "http":
		mux := http.NewServeMux()
		httpServer := &http.Server{
			Addr:              ":" + port,
			Handler:           mux,
			ReadHeaderTimeout: 3 * time.Second,
		}

		handler := mcp.NewStreamableHTTPHandler(func(_ *http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{
			Stateless:                  true,
			DisableLocalhostProtection: true,
		})
		mux.Handle("/mcp", logRequest(handler))

		return func() error {
				fmt.Printf("Serving stateless HTTPStreamable on http://localhost:%s/mcp\n", port)
				return httpServer.ListenAndServe()
			}, func() error {
				shutdownCtx, shutdownRelease := context.WithTimeout(
					context.Background(),
					1*time.Second,
				)
				defer shutdownRelease()
				return httpServer.Shutdown(shutdownCtx)
			}, nil
	default:
		return nil, nil, fmt.Errorf("unsupported transport %q, only http supported", transport)
	}
}

func helloHandler(_ context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args map[string]any
	if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil //nolint:nilerr // mcp tool errors go in result
	}
	name, err := requireStringArg(args, "name")
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil //nolint:nilerr // mcp tool errors go in result
	}

	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("Hello, %s!", name)}}}, nil
}

func headersToolHandler(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var headers http.Header
	if req.Extra != nil {
		headers = req.Extra.Header
	}

	content := make([]mcp.Content, 0)
	for k, v := range headers {
		content = append(content, &mcp.TextContent{
			Text: fmt.Sprintf("%s: %v", k, v),
		})
	}

	return &mcp.CallToolResult{Content: content}, nil
}

func greetingHandler(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	name := req.Params.Arguments["name"]
	return &mcp.GetPromptResult{
		Messages: []*mcp.PromptMessage{
			{Role: "user", Content: &mcp.TextContent{Text: fmt.Sprintf("Please greet %s warmly", name)}},
		},
	}, nil
}

func logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))

		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)

		log.Printf("[stateless-server] %s %s status=%d protocol-version=%q mcp-method=%q mcp-name=%q body=%s",
			r.Method, r.URL.Path, rec.status,
			r.Header.Get("Mcp-Protocol-Version"),
			r.Header.Get("Mcp-Method"),
			r.Header.Get("Mcp-Name"),
			truncate(string(body), 500))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// requireStringArg parses an argument with mark3labs RequireString
// semantics: any string passes, including empty.
func requireStringArg(args map[string]any, key string) (string, error) {
	val, ok := args[key]
	if !ok {
		return "", fmt.Errorf("required argument %q not found", key)
	}
	if str, ok := val.(string); ok {
		return str, nil
	}
	return "", fmt.Errorf("argument %q is not a string", key)
}

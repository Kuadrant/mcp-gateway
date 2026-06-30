// Package server2 implements a simple MCP server that implements a few tools
// - The "hello_world" tool from the library sample
// - A "time" tool that returns the current time
// - A "slow" tool that waits N seconds, notifying the client of progress
// - A "headers" tool that returns all HTTP headers it received
package server2

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/utils/ptr"
)

// StartupFunc is used for functions that will start a server and block until it is finished
type StartupFunc func() error

// ShutdownFunc is used for functions that stop running servers
type ShutdownFunc func() error

type testTool struct {
	tool    mcp.Tool
	handler mcp.ToolHandler
}

var (
	testTools = map[string]testTool{
		"hello_world": {
			tool: mcp.Tool{
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
				Annotations: &mcp.ToolAnnotations{
					Title:           "greeter tool",
					ReadOnlyHint:    true,
					DestructiveHint: ptr.To(false),
					IdempotentHint:  true,
					OpenWorldHint:   ptr.To(false),
				},
			},
			handler: helloHandler,
		},
		"time": {
			tool: mcp.Tool{
				Name:        "time",
				Description: "Get the current time",
				InputSchema: map[string]any{
					"type": "object",
				},
				Annotations: &mcp.ToolAnnotations{
					Title:           "Clock",
					ReadOnlyHint:    true,
					DestructiveHint: ptr.To(false),
					IdempotentHint:  true,
					OpenWorldHint:   ptr.To(false),
				},
			},
			handler: timeHandler,
		},
		"headers": {
			tool: mcp.Tool{
				Name:        "headers",
				Description: "get HTTP headers",
				InputSchema: map[string]any{
					"type": "object",
				},
				Annotations: &mcp.ToolAnnotations{
					Title:           "header inspector",
					ReadOnlyHint:    true,
					DestructiveHint: ptr.To(false),
					IdempotentHint:  true,
					OpenWorldHint:   ptr.To(false),
				},
			},
			handler: headersToolHandler,
		},
		"auth1234": {
			tool: mcp.Tool{
				Name:        "auth1234",
				Description: "check authorization header",
				InputSchema: map[string]any{
					"type": "object",
				},
				Annotations: &mcp.ToolAnnotations{
					Title:           "auth header verifier",
					ReadOnlyHint:    true,
					DestructiveHint: ptr.To(false),
					IdempotentHint:  true,
					OpenWorldHint:   ptr.To(false),
				},
			},
			handler: auth1234ToolHandler,
		},
		"slow": {
			tool: mcp.Tool{
				Name:        "slow",
				Description: "Delay for N seconds",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"seconds": map[string]any{
							"type":        "string",
							"description": "number of seconds to wait",
						},
					},
					"required": []string{"seconds"},
				},
				Annotations: &mcp.ToolAnnotations{
					Title:           "delay tool",
					ReadOnlyHint:    true,
					DestructiveHint: ptr.To(false),
					IdempotentHint:  true,
					OpenWorldHint:   ptr.To(false),
				},
			},
			handler: slowHandler,
		},
		"set_time": {
			tool: mcp.Tool{
				Name:        "set_time",
				Description: "Set the clock",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"time": map[string]any{
							"type":        "string",
							"description": "new time",
						},
					},
					"required": []string{"time"},
				},
				Annotations: &mcp.ToolAnnotations{
					Title:           "set time tool",
					DestructiveHint: ptr.To(true),
					IdempotentHint:  true,
					OpenWorldHint:   ptr.To(false),
				},
			},
			handler: setTimeHandler,
		},
		"pour_chocolate_into_mold": {
			tool: mcp.Tool{
				Name:        "pour_chocolate_into_mold",
				Description: "Pour chocolate into mold",
				InputSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"quantity": map[string]any{
							"type":        "string",
							"description": "milliliters",
						},
					},
					"required": []string{"quantity"},
				},
				Annotations: &mcp.ToolAnnotations{
					Title:           "chocolate fill tool",
					DestructiveHint: ptr.To(true),
					OpenWorldHint:   ptr.To(true),
				},
			},
			handler: pourChocolateHandler,
		},
	}
)

// RunServer creates a server that can be started and stopped
func RunServer(transport, port string) (StartupFunc, ShutdownFunc, error) {
	s := mcp.NewServer(&mcp.Implementation{Name: "Demo rocket", Version: "1.0.0"}, &mcp.ServerOptions{})

	for _, tt := range testTools {
		s.AddTool(&tt.tool, tt.handler)
	}

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
			DisableLocalhostProtection: true,
		})
		mux.Handle("/mcp", logResponse(handler))

		mux.HandleFunc("/admin/forget", forgetFuncFactory(s))
		mux.HandleFunc("/admin/deleteTool", deleteToolFactory(s))
		mux.HandleFunc("/admin/addTool", addToolFactory(s))

		return func() error {
				fmt.Printf("Serving HTTPStreamable on http://localhost:%s/mcp\n", port)
				return httpServer.ListenAndServe()
			}, func() error {
				shutdownCtx, shutdownRelease := context.WithTimeout(
					context.Background(),
					1*time.Second,
				)
				defer shutdownRelease()
				return httpServer.Shutdown(shutdownCtx)
			}, nil
	case "sse":
		return nil, nil, fmt.Errorf("sse transport not supported with official SDK")
	default:
		return nil, nil, fmt.Errorf("stdio transport not supported with official SDK")
	}
}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *responseRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the underlying writer's Flush;
// without it SSE headers sit in the server buffer and clients hang.
func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// Flush forwards to the underlying writer for direct http.Flusher assertions.
func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func logResponse(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		reqSession := r.Header.Get("Mcp-Session-Id")
		if reqSession == "" {
			reqSession = "-"
		}
		respSession := rec.Header().Get("Mcp-Session-Id")
		if respSession == "" {
			respSession = "-"
		}
		clientID := r.Header.Get("X-Client-Id")
		if clientID == "" {
			clientID = "-"
		}
		// quoting attacker-controllable header values prevents log forging
		log.Printf("%q %q %d req-session=%q resp-session=%q x-client-id=%q", r.Method, r.URL.Path, rec.status, reqSession, respSession, clientID)
	})
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

func timeHandler(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: time.Now().String()}}}, nil
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

func auth1234ToolHandler(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var headers http.Header
	if req.Extra != nil {
		headers = req.Extra.Header
	}

	auth := strings.ToLower(headers.Get("Authorization"))
	if auth != "bearer 1234" {
		return nil, fmt.Errorf("requires Authorization: bearer 1234, got %q", auth)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: "Success!",
			},
		},
	}, nil
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

// requireIntArg parses an argument with mark3labs RequireInt semantics:
// numbers and numeric strings both convert.
func requireIntArg(args map[string]any, key string) (int, error) {
	val, ok := args[key]
	if !ok {
		return 0, fmt.Errorf("required argument %q not found", key)
	}
	switch v := val.(type) {
	case int:
		return v, nil
	case float64:
		return int(v), nil
	case string:
		if i, err := strconv.Atoi(v); err == nil {
			return i, nil
		}
		return 0, fmt.Errorf("argument %q cannot be converted to int", key)
	default:
		return 0, fmt.Errorf("argument %q is not an int", key)
	}
}

func slowHandler(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args map[string]any
	if err := json.Unmarshal(request.Params.Arguments, &args); err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil //nolint:nilerr // mcp tool errors go in result
	}
	seconds, err := requireIntArg(args, "seconds")
	if err != nil {
		return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil //nolint:nilerr // mcp tool errors go in result
	}

	progressToken := request.Params.GetProgressToken()

	startTime := time.Now()
	fmt.Printf("Slow tool will wait for %d seconds\n", seconds)
	for {
		waited := int(time.Since(startTime).Seconds())
		if waited >= seconds {
			break
		}

		if progressToken != nil {
			fmt.Printf("Notify client that we have waited %d seconds\n", waited)
			err := request.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: progressToken,
				Progress:      float64(waited),
				Message:       fmt.Sprintf("Waited %d seconds...", waited),
			})
			if err != nil {
				fmt.Printf("NotifyProgress error: %v\n", err)
			}
		}

		time.Sleep(1 * time.Second)
	}

	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil
}

// setTimeHandler demonstrates a tool that is "destructive"
func setTimeHandler(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: "Error: setting of time unimplemented",
			},
		},
		IsError: true,
	}, nil
}

// pourChocolateHandler demonstrates a tool that is NOT idempotent
func pourChocolateHandler(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{
				Text: "Error: out of chocolate",
			},
		},
		IsError: true,
	}, nil
}

// forgetFuncFactory closes the server session with the posted ID, so the
// server genuinely forgets it (subsequent requests 404), matching the
// mark3labs UnregisterSession behaviour.
func forgetFuncFactory(mcpServer *mcp.Server) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failure: %v", err), http.StatusInternalServerError)
			return
		}
		err = req.Body.Close()
		if err != nil {
			log.Printf("/admin/forget failed to close: %v\n", err)
		}

		sessionID := string(body)
		forgotten := false
		for ss := range mcpServer.Sessions() {
			if ss.ID() == sessionID {
				if err := ss.Close(); err != nil {
					log.Printf("/admin/forget close failed for %s: %v", sessionID, err)
				}
				forgotten = true
			}
		}
		log.Printf("Client %s forget requested (forgotten=%v)", sessionID, forgotten)
	}
}

func addToolFactory(mcpServer *mcp.Server) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failure: %v", err), http.StatusInternalServerError)
			return
		}
		err = req.Body.Close()
		if err != nil {
			log.Printf("/admin/addTool failed to close: %v\n", err)
		}

		tt, ok := testTools[string(body)]
		if !ok {
			http.Error(w, fmt.Sprintf("Unknown tool %q", body), http.StatusNotFound)
			return
		}

		log.Printf("Adding tool %q\n", body)
		mcpServer.AddTool(&tt.tool, tt.handler)
	}
}

func deleteToolFactory(mcpServer *mcp.Server) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodDelete {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(
				w,
				fmt.Sprintf("MCP Tool delete needs tool name body: %v", err),
				http.StatusInternalServerError,
			)
			return
		}
		err = req.Body.Close()
		if err != nil {
			log.Printf("/admin/deleteTool failed to close: %v\n", err)
		}

		toolName := string(body)
		_, ok := testTools[string(body)]
		if !ok {
			http.Error(w, fmt.Sprintf("Unknown tool %q", toolName), http.StatusNotFound)
			return
		}

		log.Printf("Deleting tool %q\n", toolName)
		mcpServer.RemoveTools(toolName)
	}
}

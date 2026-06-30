// Based on sample https://github.com/mark3labs/mcp-go/blob/93935261086dda133e3e4b6447304e24deb56a21/www/docs/pages/servers/basics.mdx

// Package server2 implements a simple MCP server that implements a few tools
// - The "hello_world" tool from the library sample
// - A "time" tool that returns the current time
// - A "slow" tool that waits N seconds, notifying the client of progress
// - A "headers" tool that returns all HTTP headers it received
package server2

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// StartupFunc is used for functions that will start a server and block until it is finished
type StartupFunc func() error

// ShutdownFunc is used for functions that stop running servers
type ShutdownFunc func() error

var (
	testTools = map[string]server.ServerTool{
		"hello_world": {
			Tool: mcp.NewTool("hello_world",
				mcp.WithDescription("Say hello to someone"),
				mcp.WithString("name",
					mcp.Required(),
					mcp.Description("Name of the person to greet"),
				),
				mcp.WithTitleAnnotation("greeter tool"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
			),
			Handler: helloHandler,
		},
		"time": {
			Tool: mcp.NewTool("time",
				mcp.WithDescription("Get the current time"),
				mcp.WithTitleAnnotation("Clock"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
			),
			Handler: timeHandler,
		},
		"headers": {
			Tool: mcp.NewTool("headers",
				mcp.WithDescription("get HTTP headers"),
				mcp.WithTitleAnnotation("header inspector"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
			),
			Handler: headersToolHandler,
		},
		"auth1234": {
			Tool: mcp.NewTool("auth1234",
				mcp.WithDescription("check authorization header"),
				mcp.WithTitleAnnotation("auth header verifier"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
			),
			Handler: auth1234ToolHandler,
		},
		"slow": {
			Tool: mcp.NewTool("slow",
				mcp.WithDescription("Delay for N seconds"),
				mcp.WithTitleAnnotation("delay tool"),
				mcp.WithReadOnlyHintAnnotation(true),
				mcp.WithDestructiveHintAnnotation(false),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("seconds",
					mcp.Required(),
					mcp.Description("number of seconds to wait"),
				)),
			Handler: slowHandler,
		},
		"set_time": {
			Tool: mcp.NewTool("set_time",
				mcp.WithDescription("Set the clock"),
				mcp.WithTitleAnnotation("set time tool"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(true),
				mcp.WithIdempotentHintAnnotation(true),
				mcp.WithOpenWorldHintAnnotation(false),
				mcp.WithString("time",
					mcp.Required(),
					mcp.Description("new time"),
				)),
			Handler: setTimeHandler,
		},
		"pour_chocolate_into_mold": {
			Tool: mcp.NewTool("pour_chocolate_into_mold",
				mcp.WithDescription("Pour chocolate into mold"),
				mcp.WithTitleAnnotation("chocolate fill tool"),
				mcp.WithReadOnlyHintAnnotation(false),
				mcp.WithDestructiveHintAnnotation(true),
				mcp.WithIdempotentHintAnnotation(false),
				mcp.WithOpenWorldHintAnnotation(true),
				mcp.WithString("quantity",
					mcp.Required(),
					mcp.Description("milliliters"),
				)),
			Handler: pourChocolateHandler,
		},
	}
)

// RunServer create a server that can be started and stopped
func RunServer(transport, port string, streamOpts ...server.StreamableHTTPOption) (StartupFunc, ShutdownFunc, error) {

	hooks := &server.Hooks{}

	// Note that AddOnRegisterSession is for GET, not POST, for a session.
	// https://modelcontextprotocol.io/specification/2025-03-26/basic/transports#listening-for-messages-from-the-server
	hooks.AddOnRegisterSession(func(_ context.Context, session server.ClientSession) {
		log.Printf("Client %s connected", session.SessionID())
	})

	hooks.AddOnUnregisterSession(func(_ context.Context, session server.ClientSession) {
		log.Printf("Client %s disconnected", session.SessionID())
	})

	hooks.AddBeforeAny(func(ctx context.Context, _ any, method mcp.MCPMethod, _ any) {
		sessionID := "-"
		if s := server.ClientSessionFromContext(ctx); s != nil {
			sessionID = s.SessionID()
		}
		log.Printf("Processing %s session=%s", method, sessionID)
	})

	hooks.AddOnSuccess(func(_ context.Context, _ any, method mcp.MCPMethod, _ any, _ any) {
		log.Printf("Completed %s", method)
	})

	hooks.AddOnError(func(_ context.Context, _ any, method mcp.MCPMethod, _ any, err error) {
		log.Printf("Error in %s: %v", method, err)
	})

	// Create a new MCP server
	s := server.NewMCPServer(
		"Demo rocket",
		"1.0.0",
		server.WithHooks(hooks),
		server.WithToolCapabilities(true),
	)

	for _, tool := range testTools {
		s.AddTools(tool)
	}

	if port == "" {
		port = "8080"
	}

	switch transport {
	case "http":
		// Define the HTTP server with interceptor to record HTTP headers
		mux := http.NewServeMux()
		httpServer := &http.Server{
			Addr:              ":" + port,
			Handler:           mux,
			ReadHeaderTimeout: 3 * time.Second,
		}

		streamOpts = append(streamOpts, server.WithStreamableHTTPServer(httpServer))
		streamableHTTPServer := server.NewStreamableHTTPServer(
			s,
			streamOpts...,
		)
		mux.Handle("/mcp", logResponse(streamableHTTPServer))

		// For testing session ID invalidation
		mux.HandleFunc("/admin/forget", forgetFuncFactory(s))
		mux.HandleFunc("/admin/deleteTool", deleteToolFactory(s))
		mux.HandleFunc("/admin/addTool", addToolFactory(s))

		return func() error {
				fmt.Printf("Serving HTTPStreamable on http://localhost:%s/mcp\n", port)

				return streamableHTTPServer.Start(":" + port)
			}, func() error {
				// We use a timeout because the MCP inspector holds the port open
				shutdownCtx, shutdownRelease := context.WithTimeout(
					context.Background(),
					1*time.Second,
				)
				defer shutdownRelease()
				return streamableHTTPServer.Shutdown(shutdownCtx)
			}, nil
	case "sse":
		fmt.Printf("Serving SSE on http://localhost:%s\n", port)
		sseServer := server.NewSSEServer(s)

		return func() error {
				return sseServer.Start(":" + port)
			}, func() error {
				return sseServer.Shutdown(context.TODO())
			}, nil
	default:
		fmt.Print("Serving on stdio")
		return func() error {
				return server.ServeStdio(s)
			}, func() error {
				return nil
			}, nil
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
		log.Printf("%s %s %d req-session=%s resp-session=%s x-client-id=%s", r.Method, r.URL.Path, rec.status, reqSession, respSession, clientID)
	})
}

func helloHandler(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := request.RequireString("name")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf("Hello, %s!", name)), nil
}

func timeHandler(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText(time.Now().String()), nil
}

func headersToolHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	content := make([]mcp.Content, 0)
	for k, v := range req.Header {
		content = append(content, &mcp.TextContent{
			Type: "text",
			Text: fmt.Sprintf("%s: %v", k, v),
		})
	}

	return &mcp.CallToolResult{
		Content: content}, nil
}

func auth1234ToolHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {

	auth := strings.ToLower(req.Header.Get("Authorization"))
	if auth != "bearer 1234" {
		return nil, fmt.Errorf("requires Authorization: bearer 1234, got %q", auth)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{
				Text: "Success!",
			},
		},
	}, nil
}

func slowHandler(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	seconds, err := request.RequireInt("seconds")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	var progressToken mcp.ProgressToken
	if request.Params.Meta != nil {
		progressToken = request.Params.Meta.ProgressToken
	}
	server := server.ServerFromContext(ctx)

	startTime := time.Now()
	fmt.Printf("Slow tool will wait for %d seconds\n", seconds)
	for {
		waited := int(time.Since(startTime).Seconds())
		if waited >= seconds {
			break
		}

		if progressToken != nil {
			fmt.Printf("Notify client that we have waited %d seconds\n", waited)
			msg := fmt.Sprintf("Waited %d seconds...", waited)
			err := server.SendNotificationToClient(ctx, "notifications/progress", map[string]any{
				"progress":      waited,
				"progressToken": progressToken,
				"message":       msg,
			})
			if err != nil {
				fmt.Printf("NotifyProgress error: %v\n", err)
			}
		}

		time.Sleep(1 * time.Second)
	}

	return mcp.NewToolResultText("done"), nil
}

// setTimeHandler demonstrates a tool that is "destructive"
func setTimeHandler(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{
				Type: "text",
				Text: "Error: setting of time unimplemented",
			},
		},
		IsError: true,
	}, nil
}

// pourChocolateHandler demonstrates a tool that is NOT idempotent
// (pouring chocolate twice will overflow the mold)
func pourChocolateHandler(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{
				Type: "text",
				Text: "Error: out of chocolate",
			},
		},
		IsError: true,
	}, nil
}

func forgetFuncFactory(mcpServer *server.MCPServer) func(http.ResponseWriter, *http.Request) {
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

		// We can't check if the client exists
		log.Printf("Client %s will be forcibly disconnected (if it exists)", sessionID)
		mcpServer.UnregisterSession(req.Context(), sessionID)
	}
}

func addToolFactory(mcpServer *server.MCPServer) func(http.ResponseWriter, *http.Request) {
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

		tool, ok := testTools[string(body)]
		if !ok {
			http.Error(w, fmt.Sprintf("Unknown tool %q", body), http.StatusNotFound)
			return
		}

		log.Printf("Adding tool %q\n", body)
		mcpServer.AddTools(tool)
	}
}

func deleteToolFactory(mcpServer *server.MCPServer) func(http.ResponseWriter, *http.Request) {
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
			log.Printf("/admin/forget failed to close: %v\n", err)
		}

		toolName := string(body)
		_, ok := testTools[string(body)]
		if !ok {
			http.Error(w, fmt.Sprintf("Unknown tool %q", toolName), http.StatusNotFound)
			return
		}

		// mcpServer does not return an error or let us check if a tool doesn't exist,
		// so we always return OK
		log.Printf("Deleting tool %q\n", toolName)
		mcpServer.DeleteTools(toolName)
	}
}

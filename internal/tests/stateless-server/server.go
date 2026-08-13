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
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	userAToken = "bearer user-a-token"
	userBToken = "bearer user-b-token" //nolint:gosec // test credentials
)

var perUserTools = map[string][]string{
	userAToken: {"list_repos"},
	userBToken: {"run_pipeline"},
}

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

	// user-specific tools (visible only to the matching bearer token)
	s.AddTool(&mcp.Tool{
		Name:        "list_repos",
		Description: "List repositories for the authenticated user",
		InputSchema: map[string]any{"type": "object"},
	}, stubToolHandler("list_repos"))
	s.AddTool(&mcp.Tool{
		Name:        "run_pipeline",
		Description: "Trigger a CI/CD pipeline run",
		InputSchema: map[string]any{"type": "object"},
	}, stubToolHandler("run_pipeline"))

	// add_tool dynamically adds a tool, triggering notifications/tools/list_changed
	s.AddTool(&mcp.Tool{
		Name:        "add_tool",
		Description: "dynamically add a new tool (triggers notifications/tools/list_changed)",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":        map[string]any{"type": "string", "description": "tool name"},
				"description": map[string]any{"type": "string", "description": "tool description"},
			},
			"required": []string{"name"},
		},
	}, addToolMCPHandler(s))

	// trigger-elicitation-request returns InputRequiredResult on first call,
	// completes when the client retries with InputResponses (MRTR pattern)
	s.AddTool(&mcp.Tool{
		Name:        "trigger-elicitation-request",
		Description: "trigger an elicitation request from the server",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}, elicitationToolHandler())

	s.AddReceivingMiddleware(userToolFilterMiddleware(), cacheMetadataMiddleware())

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
		mux.HandleFunc("/admin/addTool", addToolHandler(s))
		mux.HandleFunc("/admin/deleteTool", deleteToolHandler(s))

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

func stubToolHandler(name string) mcp.ToolHandler {
	return func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%s: ok", name)}}}, nil
	}
}

func userToolFilterMiddleware() mcp.Middleware {
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
			auth := strings.ToLower(headers.Get("Authorization"))
			if _, isPerUser := perUserTools[auth]; isPerUser {
				allowed := make(map[string]bool)
				for _, tool := range toolsResult.Tools {
					allowed[tool.Name] = true
				}
				// remove tools that belong to other users
				for token, tools := range perUserTools {
					if token == auth {
						continue
					}
					for _, name := range tools {
						delete(allowed, name)
					}
				}
				filtered := make([]*mcp.Tool, 0, len(allowed))
				for _, tool := range toolsResult.Tools {
					if allowed[tool.Name] {
						filtered = append(filtered, tool)
					}
				}
				toolsResult.Tools = filtered
			} else if auth == "" {
				// no auth: hide per-user tools, show everything else
				hidden := make(map[string]bool)
				for _, tools := range perUserTools {
					for _, name := range tools {
						hidden[name] = true
					}
				}
				filtered := make([]*mcp.Tool, 0, len(toolsResult.Tools))
				for _, tool := range toolsResult.Tools {
					if !hidden[tool.Name] {
						filtered = append(filtered, tool)
					}
				}
				toolsResult.Tools = filtered
			}
			if auth != "" {
				toolsResult.CacheScope = "private"
			}
			return result, nil
		}
	}
}

// cacheMetadataMiddleware sets TTLMs and CacheScope on tools/list and
// prompts/list responses. Values are configurable via env vars:
// MCP_TOOLS_TTL_MS (default 60000), MCP_TOOLS_CACHE_SCOPE (default "public"),
// MCP_PROMPTS_TTL_MS (default 60000), MCP_PROMPTS_CACHE_SCOPE (default "public").
func cacheMetadataMiddleware() mcp.Middleware {
	toolsTTL := envInt("MCP_TOOLS_TTL_MS", 60000)
	toolsScope := envStr("MCP_TOOLS_CACHE_SCOPE", "public")
	promptsTTL := envInt("MCP_PROMPTS_TTL_MS", 60000)
	promptsScope := envStr("MCP_PROMPTS_CACHE_SCOPE", "public")

	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			result, err := next(ctx, method, req)
			if err != nil {
				return result, err
			}
			switch method {
			case "tools/list":
				if tr, ok := result.(*mcp.ListToolsResult); ok && tr != nil {
					tr.TTLMs = toolsTTL
					tr.CacheScope = toolsScope
				}
			case "prompts/list":
				if pr, ok := result.(*mcp.ListPromptsResult); ok && pr != nil {
					pr.TTLMs = promptsTTL
					pr.CacheScope = promptsScope
				}
			}
			return result, nil
		}
	}
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func addToolMCPHandler(s *mcp.Server) mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]any
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return stubToolResult("invalid arguments"), err
		}
		name, _ := args["name"].(string)
		if name == "" {
			return stubToolResult("name required"), nil
		}
		desc, _ := args["description"].(string)
		if desc == "" {
			desc = "dynamically added tool"
		}
		log.Printf("add_tool: adding %q", name)
		s.AddTool(&mcp.Tool{
			Name:        name,
			Description: desc,
			InputSchema: map[string]any{"type": "object"},
		}, stubToolHandler(name))
		return stubToolResult(fmt.Sprintf("added tool %s", name)), nil
	}
}

func stubToolResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func addToolHandler(s *mcp.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = r.Body.Close()
		name := strings.TrimSpace(string(body))
		if name == "" {
			http.Error(w, "tool name required", http.StatusBadRequest)
			return
		}
		log.Printf("admin: adding tool %q", name)
		s.AddTool(&mcp.Tool{
			Name:        name,
			Description: "dynamically added tool",
			InputSchema: map[string]any{"type": "object"},
		}, stubToolHandler(name))
		w.WriteHeader(http.StatusCreated)
	}
}

func deleteToolHandler(s *mcp.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = r.Body.Close()
		name := strings.TrimSpace(string(body))
		if name == "" {
			http.Error(w, "tool name required", http.StatusBadRequest)
			return
		}
		log.Printf("admin: deleting tool %q", name)
		s.RemoveTools(name)
		w.WriteHeader(http.StatusOK)
	}
}

func elicitationToolHandler() mcp.ToolHandler {
	return func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if len(req.Params.InputResponses) == 0 {
			return &mcp.CallToolResult{
				InputRequests: mcp.InputRequestMap{
					"user_info": &mcp.ElicitParams{
						Message: "Please provide your information",
						RequestedSchema: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"name": map[string]any{
									"type":        "string",
									"description": "Your name",
								},
							},
							"required": []string{"name"},
						},
					},
				},
				RequestState: "elicitation-pending",
			}, nil
		}

		resp, ok := req.Params.InputResponses["user_info"]
		if !ok {
			return &mcp.CallToolResult{
				InputRequests: mcp.InputRequestMap{
					"user_info": &mcp.ElicitParams{Message: "Please provide your information (retry)"},
				},
				RequestState: req.Params.RequestState,
			}, nil
		}

		elicitResult, ok := resp.(*mcp.ElicitResult)
		if !ok || elicitResult == nil {
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: "invalid elicitation response"}},
			}, nil
		}

		switch elicitResult.Action {
		case "decline", "cancel":
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("User %sed the elicitation request", elicitResult.Action)}},
			}, nil
		default:
			name, _ := elicitResult.Content["name"].(string)
			if name == "" {
				name = "unknown"
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("User provided the requested information. Name: %s", name)}},
			}, nil
		}
	}
}

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

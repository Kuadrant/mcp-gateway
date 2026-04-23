// oidc-test-server is an MCP test server that authenticates requests with an OIDC provider
// It validates Authorization headers for all requests
// The value must be a valid Bearer token issued by the configured OIDC provider (ISSUER_URL env var)
// The expected audience must match the configured value (EXPECTED_AUD env var)
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
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
	Handler     http.Handler
	IssuerUrl   string
	ExpectedAud string
	AdminToken  string

	mu       sync.RWMutex
	provider *oidc.Provider
}

func (a *authMiddleware) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if os.Getenv("LOG_LEVEL") == "debug" {
		log.Printf("Received request: %s %s", req.Method, req.URL.Path)
		for h, values := range req.Header {
			for _, v := range values {
				log.Printf("\tHeader: %s: %s", h, v)
			}
		}
	}

	auth := strings.Split(req.Header.Get("Authorization"), " ")
	if len(auth) != 2 {
		http.Error(w, `{"error": "Unauthorized: missing or invalid Authorization header"}`, http.StatusUnauthorized)
		return
	}

	token := auth[1]

	switch auth[0] {
	case "Bearer":
		provider, err := a.getProvider(req.Context())
		if err != nil {
			log.Printf("Failed to get OIDC provider: %v", err)
			http.Error(w, `{"error": "Internal Server Error"}`, http.StatusInternalServerError)
			return
		}

		a.mu.RLock()
		defer a.mu.RUnlock()

		// verify token
		verifier := provider.Verifier(&oidc.Config{SkipClientIDCheck: true, SkipIssuerCheck: true})
		accessToken, err := verifier.Verify(req.Context(), token)

		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error": "Unauthorized: %v"}`, err), http.StatusUnauthorized)
			return
		}

		var claims struct {
			Aud interface{} `json:"aud"` // can be string or []string
			Sub string      `json:"sub"`
		}
		if err := accessToken.Claims(&claims); err != nil {
			log.Printf("Failed to parse claims: %v", err)
			http.Error(w, `{"error": "Unauthorized: failed to parse claims"}`, http.StatusUnauthorized)
			return
		}

		// check audience - handle both string and array
		audMatch := false
		switch aud := claims.Aud.(type) {
		case string:
			audMatch = aud == a.ExpectedAud
		case []interface{}:
			for _, audItem := range aud {
				if str, ok := audItem.(string); ok && str == a.ExpectedAud {
					audMatch = true
					break
				}
			}
		}

		if !audMatch {
			log.Printf("Invalid audience: %v", claims.Aud)
			http.Error(w, `{"error": "Unauthorized: invalid audience"}`, http.StatusUnauthorized)
			return
		}

		log.Printf("Authenticated request from subject: %s", claims.Sub)
	case "APIKEY":
		if a.AdminToken == "" || token != a.AdminToken {
			http.Error(w, `{"error": "Unauthorized: invalid API key"}`, http.StatusUnauthorized)
			return
		}

		log.Print("Authenticated request from admin user")
	default:
		http.Error(w, `{"error": "Unauthorized: unsupported authentication scheme"}`, http.StatusUnauthorized)
		return
	}

	a.Handler.ServeHTTP(w, req)
}

func (a *authMiddleware) getProvider(ctx context.Context) (*oidc.Provider, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.provider != nil {
		return a.provider, nil
	}
	return oidc.NewProvider(ctx, a.IssuerUrl)
}

func main() {
	// get config from environment
	port := os.Getenv("PORT")
	if port == "" {
		port = "9090"
	}

	// create MCP server
	server := mcp.NewServer(&mcp.Implementation{Name: "mcp-oidc-test-server"}, nil)

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
		Handler:     handler,
		IssuerUrl:   os.Getenv("ISSUER_URL"),
		ExpectedAud: os.Getenv("EXPECTED_AUD"),
		AdminToken:  os.Getenv("EXPECTED_ADMIN_TOKEN"),
	}

	// setup http server
	mux := http.NewServeMux()
	mux.Handle("/mcp", &authHandler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	log.Printf("OIDC test server listening on :%s", port)
	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 3 * time.Second,
	}

	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}

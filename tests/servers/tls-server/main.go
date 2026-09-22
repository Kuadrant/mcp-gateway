// test server with native TLS support for testing custom CA certificate functionality
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	httpAddr     = flag.String("http", "", "listen address (e.g. :8443)")
	tlsCert      = flag.String("tls-cert", "", "path to TLS certificate file")
	tlsKey       = flag.String("tls-key", "", "path to TLS private key file")
	healthAddr   = flag.String("health", ":8080", "plain HTTP health check address")
	oauthClients = flag.String("oauth-clients", "",
		"comma-separated client_id:client_secret pairs; enables /token and the protected MCP listener")
)

const (
	// the protected MCP endpoint is plain HTTP so client traffic routes through
	// Envoy like every other test server. only the token endpoint needs TLS.
	protectedAddr        = ":9090"
	tokenLifetimeSeconds = 300
)

type echoArgs struct {
	Message string `json:"message" jsonschema:"the message to echo back"`
}

func echoTool(
	_ context.Context,
	_ *mcp.CallToolRequest,
	params echoArgs,
) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf("TLS echo: %s", params.Message)},
		},
	}, nil, nil
}

func tlsInfoTool(
	_ context.Context,
	_ *mcp.CallToolRequest,
	_ struct{},
) (*mcp.CallToolResult, any, error) {
	mode := "plain HTTP"
	if *tlsCert != "" && *tlsKey != "" {
		mode = "TLS"
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: fmt.Sprintf("Server mode: %s, time: %s", mode, time.Now().Format(time.RFC3339))},
		},
	}, nil, nil
}

// clientStore holds the configured client credentials and the tokens issued to
// them. only populated when -oauth-clients is set.
type clientStore struct {
	creds  map[string]string // client_id -> client_secret
	mu     sync.RWMutex
	issued map[string]string // access token -> client_id
}

func newClientStore(creds map[string]string) *clientStore {
	return &clientStore{creds: creds, issued: make(map[string]string)}
}

// parseClients reads "id:secret[,id:secret...]".
func parseClients(spec string) (map[string]string, error) {
	creds := make(map[string]string)
	for _, pair := range strings.Split(spec, ",") {
		id, secret, ok := strings.Cut(pair, ":")
		if !ok || id == "" || secret == "" {
			return nil, fmt.Errorf("invalid client %q, want id:secret", pair)
		}
		creds[id] = secret
	}
	return creds, nil
}

func (s *clientStore) issue(clientID string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	s.mu.Lock()
	s.issued[token] = clientID
	s.mu.Unlock()
	return token, nil
}

func (s *clientStore) clientFor(token string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.issued[token]
	return id, ok
}

// handleToken implements the RFC 6749 client credentials grant.
func (s *clientStore) handleToken(w http.ResponseWriter, r *http.Request) {
	id, secret := clientCredentials(r)
	want, known := s.creds[id]
	if !known || secret != want || r.PostFormValue("grant_type") != "client_credentials" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid_client"})
		return
	}
	token, err := s.issue(id)
	if err != nil {
		http.Error(w, "token generation failed", http.StatusInternalServerError)
		return
	}
	log.Printf("Issued access token to client %q", id)
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   tokenLifetimeSeconds,
	})
}

// clientCredentials reads client_secret_basic, then client_secret_post —
// x/oauth2 probes both. the basic halves are URL-encoded before base64
// (RFC 6749 2.3.1), which Request.BasicAuth does not undo.
func clientCredentials(r *http.Request) (string, string) {
	if id, secret, ok := r.BasicAuth(); ok {
		if decoded, err := url.QueryUnescape(id); err == nil {
			id = decoded
		}
		if decoded, err := url.QueryUnescape(secret); err == nil {
			secret = decoded
		}
		return id, secret
	}
	return r.PostFormValue("client_id"), r.PostFormValue("client_secret")
}

// protectedHandler serves /mcp behind a guard that rejects anything but a token
// this process issued.
func (s *clientStore) protectedHandler() http.Handler {
	handler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		clientID, _ := s.clientFor(bearerToken(r))
		return protectedServer(clientID)
	}, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", s.requireToken(handler))
	return mux
}

func (s *clientStore) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := s.clientFor(bearerToken(r)); !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "invalid_token"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// protectedServer is a separate mcp.Server so the tools on the TLS listener are
// untouched. whoami's description names the client the presented token belongs
// to — the only place that identity is observable from outside, since a client's
// tools/call never carries the broker's token.
func protectedServer(clientID string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "test-tls-server-protected",
		Version: "0.0.1",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "whoami",
		Description: fmt.Sprintf("returns the client_id this token was issued to (%s)", clientID),
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: clientID}},
		}, nil, nil
	})
	return server
}

// writeJSON sets application/json, which x/oauth2 needs to parse the token
// response as JSON rather than form values.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("Write response failed: %v", err)
	}
}

func main() {
	flag.Parse()

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "test-tls-server",
		Version: "0.0.1",
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo_tls",
		Description: "Echo a message from the TLS test server",
	}, echoTool)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "tls_info",
		Description: "Get TLS status of this server",
	}, tlsInfoTool)

	var clients *clientStore
	if *oauthClients != "" {
		creds, err := parseClients(*oauthClients)
		if err != nil {
			log.Fatalf("Invalid -oauth-clients: %v", err)
		}
		clients = newClientStore(creds)
		go func() {
			protected := &http.Server{
				Addr:              protectedAddr,
				Handler:           clients.protectedHandler(),
				ReadHeaderTimeout: 3 * time.Second,
			}
			log.Printf("Protected MCP server listening at %s/mcp", protectedAddr)
			if err := protected.ListenAndServe(); err != nil {
				log.Fatalf("Protected server failed: %v", err)
			}
		}()
	}

	if *httpAddr != "" {
		go func() {
			healthMux := http.NewServeMux()
			healthMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			healthServer := &http.Server{
				Addr:              *healthAddr,
				Handler:           healthMux,
				ReadHeaderTimeout: 3 * time.Second,
			}
			log.Printf("Health check listening at %s/healthz", *healthAddr)
			if err := healthServer.ListenAndServe(); err != nil {
				log.Printf("Health server error: %v", err)
			}
		}()

		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
			return server
		}, nil)

		mux := http.NewServeMux()
		mux.Handle("/mcp", handler)
		if clients != nil {
			mux.HandleFunc("POST /token", clients.handleToken)
			log.Printf("Token endpoint listening at %s/token", *httpAddr)
		}
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				w.WriteHeader(http.StatusOK)
				fmt.Fprintf(w, "TLS test server\n")
			} else {
				http.NotFound(w, r)
			}
		})

		srv := &http.Server{
			Addr:              *httpAddr,
			Handler:           mux,
			ReadHeaderTimeout: 3 * time.Second,
		}

		if (*tlsCert != "") != (*tlsKey != "") {
			log.Fatalf("Both -tls-cert and -tls-key must be provided together")
		}

		if *tlsCert != "" && *tlsKey != "" {
			log.Printf("TLS server listening at %s with cert=%s", *httpAddr, *tlsCert)
			if err := srv.ListenAndServeTLS(*tlsCert, *tlsKey); err != nil {
				log.Fatalf("TLS server failed: %v", err)
			}
		} else {
			log.Printf("Plain HTTP server listening at %s", *httpAddr)
			if err := srv.ListenAndServe(); err != nil {
				log.Fatalf("Server failed: %v", err)
			}
		}
	} else {
		log.Printf("TLS test server using stdio")
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatalf("Error running server: %v", err)
		}
	}
}

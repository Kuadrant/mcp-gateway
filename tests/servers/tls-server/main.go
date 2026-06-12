// test server with native TLS support for testing custom CA certificate functionality
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	httpAddr   = flag.String("http", "", "listen address (e.g. :8443)")
	tlsCert    = flag.String("tls-cert", "", "path to TLS certificate file")
	tlsKey     = flag.String("tls-key", "", "path to TLS private key file")
	healthAddr = flag.String("health", ":8080", "plain HTTP health check address")
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

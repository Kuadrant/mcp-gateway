// stateless-server is a test MCP server that uses the 2026-07-28 stateless protocol.
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	statelessserver "github.com/Kuadrant/mcp-gateway/internal/tests/stateless-server"
)

func main() {
	transport := os.Getenv("MCP_TRANSPORT")
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	startFunc, shutdownFunc, err := statelessserver.RunServer(transport, port)
	if err != nil {
		fmt.Printf("Server error: %v\n", err)
		return
	}

	go func() {
		_ = startFunc()
	}()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
	log.Println("Shutting down server...")
	err = shutdownFunc()
	if err != nil {
		fmt.Printf("Shutdown error: %v\n", err)
		return
	}

	fmt.Print("Server completed\n")
}

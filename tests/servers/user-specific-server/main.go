// user-specific-server is a test MCP server that returns different tools per user
package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	userspecificserver "github.com/Kuadrant/mcp-gateway/internal/tests/user-specific-server"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9090"
	}

	startFunc, shutdownFunc, err := userspecificserver.RunServer(port)
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
	if err := shutdownFunc(); err != nil {
		fmt.Printf("Shutdown error: %v\n", err)
		return
	}
	fmt.Print("Server completed\n")
}

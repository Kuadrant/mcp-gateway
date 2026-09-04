package main

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

func TestSayHi(t *testing.T) {
	res, _, err := sayHi(context.Background(), &mcp.CallToolRequest{}, hiArgs{
		Name: "Fred",
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.NotNil(t, res)
	require.Len(t, res.Content, 1)
	require.IsType(t, &mcp.TextContent{}, res.Content[0])
	require.Equal(t, "Hi Fred", res.Content[0].(*mcp.TextContent).Text)
}

func TestTimeTool(t *testing.T) {
	res, _, err := timeTool(context.Background(), &mcp.CallToolRequest{}, struct{}{})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.NotNil(t, res)
	require.Len(t, res.Content, 1)
	require.IsType(t, &mcp.TextContent{}, res.Content[0])
}

func TestHeadersTool(t *testing.T) {
	res, _, err := headersTool(context.Background(), &mcp.CallToolRequest{}, struct{}{})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.NotNil(t, res)
	require.Len(t, res.Content, 0)
}

func TestSlowTool(t *testing.T) {
	res, _, err := slowTool(context.Background(), &mcp.CallToolRequest{}, slowArgs{})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.NotNil(t, res)
	require.Len(t, res.Content, 0)
}

func TestChecksumTool(t *testing.T) {
	testPayload := `{"key": "value", "unicode": "🚀", "nested": {"a": [1, 2, 3]}}`
	res, _, err := checksumTool(context.Background(), &mcp.CallToolRequest{}, checksumArgs{Payload: testPayload})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.NotNil(t, res)
	require.Len(t, res.Content, 1)
	require.IsType(t, &mcp.TextContent{}, res.Content[0])

	// sha256 of testPayload
	// calculated expected sha256 hash
	expectedHash := "8e72a3f8233be49e5422c9b03e92491fd67b371a121c9a7a04303fccac02e73e"
	require.Equal(t, expectedHash, res.Content[0].(*mcp.TextContent).Text)
}

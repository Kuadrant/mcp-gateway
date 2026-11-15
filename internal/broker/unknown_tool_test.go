package broker

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// The 2026 router forwards unknown tools/call to the broker (BrokerPass) so
// ext_proc immediate_response is avoided. The broker MCP server must then
// return JSON-RPC -32602 (Invalid params), matching the MCP unknown-tool
// protocol error the 2025 router emits locally.
func TestUnknownToolCallReturnsInvalidParams(t *testing.T) {
	b := NewBroker(slog.Default(), WithDiscoveryToolsEnabled(false)).(*mcpBrokerImpl)
	cs := connectInMemory(t, b)

	_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "no_such_tool"})
	require.Error(t, err)

	var rpcErr *jsonrpc.Error
	require.True(t, errors.As(err, &rpcErr), "expected *jsonrpc.Error from unknown tool call")
	require.EqualValues(t, jsonrpc.CodeInvalidParams, rpcErr.Code)
	require.Contains(t, rpcErr.Message, "unknown tool")
}

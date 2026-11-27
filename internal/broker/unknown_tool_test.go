package broker

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/protocol"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/stretchr/testify/require"
)

func TestUnknownToolCallReturnsInvalidParams(t *testing.T) {
	h := newBrokerHarness(t, func(h *brokerHarness) http.Handler { return h.b.MCPHandler() })
	res := h.do(t, http.MethodPost, "",
		`{"jsonrpc":"2.0","id":"unknown-tool","method":"tools/call","params":{
			"name":"no_such_tool",
			"_meta":{
				"io.modelcontextprotocol/protocolVersion":"2026-07-28",
				"io.modelcontextprotocol/clientInfo":{"name":"test","version":"1.0"},
				"io.modelcontextprotocol/clientCapabilities":{}
			}
		}}`,
		map[string]string{
			"Mcp-Protocol-Version": protocol.Version2026,
			"Mcp-Method":           "tools/call",
			"Mcp-Name":             "no_such_tool",
		})
	require.Equal(t, http.StatusBadRequest, res.status, res.body)
	require.Equal(t, "application/json", res.header.Get("Content-Type"))
	require.Empty(t, res.header.Get("Mcp-Session-Id"))

	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Error   *jsonrpc.Error  `json:"error"`
		Result  json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.body), &response))
	require.Equal(t, "2.0", response.JSONRPC)
	require.Equal(t, "unknown-tool", response.ID)
	require.Nil(t, response.Result)
	require.NotNil(t, response.Error)
	require.EqualValues(t, jsonrpc.CodeInvalidParams, response.Error.Code)
	require.Contains(t, response.Error.Message, "unknown tool")
}

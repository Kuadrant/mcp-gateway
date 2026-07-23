//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
)

// MCP 2026-07-28 raw HTTP helpers for tests that need to send deliberately
// malformed requests (e.g. header-body mismatch) that the SDK client would
// not allow. Most tests should use the SDK client via NewStatefulClient.

// mcp2026Headers returns the MCP 2026-07-28 protocol headers for a given method and name.
func mcp2026Headers(method, name string) map[string]string {
	h := map[string]string{
		"mcp-protocol-version": "2026-07-28",
		"mcp-method":           method,
	}
	if name != "" {
		h["mcp-name"] = name
	}
	return h
}

// mcp2026Payload builds a JSON-RPC body with _meta for the 2026-07-28 protocol.
func mcp2026Payload(method string, params map[string]any) ([]byte, error) {
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
		"io.modelcontextprotocol/clientInfo":         map[string]string{"name": "e2e-test", "version": "0.0.1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      jsonRPCID.Add(1),
		"method":  method,
		"params":  params,
	}
	return json.Marshal(payload)
}

// mcp2026RawPost sends a raw stateless request with custom headers and body.
func mcp2026RawPost(ctx context.Context, url string, body []byte, headers map[string]string) (int, string, error) {
	status, respBody, _, err := mcpRawPost(ctx, url, "", body, headers)
	if err != nil {
		return status, "", err
	}
	return status, respBody, nil
}

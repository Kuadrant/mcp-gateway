//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// raw MCP HTTP helpers provide direct control over the Mcp-Session-Id header,
// which the SDK clients abstract away. This is needed for tests that verify
// session affinity across multiple requests (e.g. redis-backed session routing).

var mcpHTTPClient *http.Client

func getMCPHTTPClient() *http.Client {
	if mcpHTTPClient == nil {
		if strings.ToLower(useInsecureClient) == "true" {
			mcpHTTPClient = &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
				},
			}
		} else {
			mcpHTTPClient = &http.Client{}
		}
	}
	return mcpHTTPClient
}

// mcpPost sends a raw HTTP POST to the MCP gateway and returns the response.
// Caller is responsible for closing the response body.
func mcpPost(ctx context.Context, url, sessionID string, body []byte, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return getMCPHTTPClient().Do(req)
}

// mcpInitialize sends an initialize request and returns the session ID from the response header
func mcpInitialize(ctx context.Context, url string, headers map[string]string) (string, error) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"e2e-raw","version":"0.0.1"}}}`
	resp, err := mcpPost(ctx, url, "", []byte(body), headers)
	if err != nil {
		return "", fmt.Errorf("initialize request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("initialize returned status %d", resp.StatusCode)
	}
	sessionID := resp.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		return "", fmt.Errorf("no Mcp-Session-Id in initialize response")
	}
	return sessionID, nil
}

func mcpNotifyInitialized(ctx context.Context, url, sessionID string, headers map[string]string) error {
	body := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	resp, err := mcpPost(ctx, url, sessionID, []byte(body), headers)
	if err != nil {
		return fmt.Errorf("notifications/initialized failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading notifications/initialized response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notifications/initialized returned status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func mcpListTools(ctx context.Context, url, sessionID string, headers map[string]string) (int, []string, error) {
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	resp, err := mcpPost(ctx, url, sessionID, []byte(body), headers)
	if err != nil {
		return 0, nil, fmt.Errorf("tools/list failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return resp.StatusCode, nil, fmt.Errorf("tools/list returned status %d (body unreadable: %w)", resp.StatusCode, readErr)
		}
		return resp.StatusCode, nil, fmt.Errorf("tools/list returned status %d: %s", resp.StatusCode, string(respBody))
	}

	result, err := readJSONRPCResult(resp)
	if err != nil {
		return resp.StatusCode, nil, err
	}

	var listResult struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(result, &listResult); err != nil {
		return resp.StatusCode, nil, fmt.Errorf("failed to parse tools/list result: %w: %s", err, string(result))
	}
	names := make([]string, len(listResult.Tools))
	for i, t := range listResult.Tools {
		names[i] = t.Name
	}
	return resp.StatusCode, names, nil
}

func mcpCallTool(ctx context.Context, url, sessionID, toolName string, args map[string]any, headers map[string]string) (int, []toolContent, error) {
	params := map[string]any{"name": toolName}
	if len(args) > 0 {
		params["arguments"] = args
	}
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params":  params,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to marshal tools/call: %w", err)
	}

	resp, err := mcpPost(ctx, url, sessionID, body, headers)
	if err != nil {
		return 0, nil, fmt.Errorf("tools/call failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return resp.StatusCode, nil, fmt.Errorf("tools/call returned status %d (body unreadable: %w)", resp.StatusCode, readErr)
		}
		return resp.StatusCode, nil, fmt.Errorf("tools/call returned status %d: %s", resp.StatusCode, string(respBody))
	}

	result, err := readJSONRPCResult(resp)
	if err != nil {
		return resp.StatusCode, nil, err
	}

	var callResult struct {
		Content []toolContent `json:"content"`
	}
	if err := json.Unmarshal(result, &callResult); err != nil {
		return resp.StatusCode, nil, fmt.Errorf("failed to parse tool result: %w: %s", err, string(result))
	}
	return resp.StatusCode, callResult.Content, nil
}

func mcpRawPost(ctx context.Context, url, sessionID string, body []byte, headers map[string]string) (int, string, http.Header, error) {
	resp, err := mcpPost(ctx, url, sessionID, body, headers)
	if err != nil {
		return 0, "", nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", resp.Header, fmt.Errorf("reading response body: %w", err)
	}
	return resp.StatusCode, string(respBody), resp.Header, nil
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// extractBackendSession finds the backend Mcp-Session-Id from tool content
func extractBackendSession(content []toolContent) string {
	for _, c := range content {
		if c.Type == "text" && strings.HasPrefix(c.Text, "Mcp-Session-Id") {
			return c.Text
		}
	}
	return ""
}

// readJSONRPCResult reads a JSON-RPC result from an HTTP response, handling both JSON and SSE content types
func readJSONRPCResult(resp *http.Response) (json.RawMessage, error) {
	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/event-stream") {
		return parseSSEResult(rawBody)
	}

	var msg struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(rawBody, &msg); err != nil {
		return nil, fmt.Errorf("failed to parse JSON response: %w: %s", err, string(rawBody))
	}
	if msg.Error != nil {
		return nil, fmt.Errorf("JSON-RPC error: %s", string(msg.Error))
	}
	return msg.Result, nil
}

// parseSSEResult extracts the JSON-RPC result from an SSE response body
func parseSSEResult(body []byte) (json.RawMessage, error) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var msg struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal([]byte(data), &msg) == nil && msg.Result != nil {
			return msg.Result, nil
		}
	}
	return nil, fmt.Errorf("no result found in SSE response: %s", string(body))
}

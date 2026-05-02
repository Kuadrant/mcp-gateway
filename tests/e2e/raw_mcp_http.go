//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
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
func mcpPost(url, sessionID string, body []byte, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
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
func mcpInitialize(url string, headers map[string]string) (string, error) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"e2e-raw","version":"0.0.1"}}}`
	resp, err := mcpPost(url, "", []byte(body), headers)
	if err != nil {
		return "", fmt.Errorf("initialize request failed: %w", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("initialize returned status %d", resp.StatusCode)
	}
	sessionID := resp.Header.Get("Mcp-Session-Id")
	if sessionID == "" {
		return "", fmt.Errorf("no Mcp-Session-Id in initialize response")
	}
	return sessionID, nil
}

func mcpNotifyInitialized(url, sessionID string, headers map[string]string) error {
	body := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	resp, err := mcpPost(url, sessionID, []byte(body), headers)
	if err != nil {
		return fmt.Errorf("notifications/initialized failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading notifications/initialized response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notifications/initialized returned status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func mcpListTools(url, sessionID string, headers map[string]string) (int, []string, error) {
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	resp, err := mcpPost(url, sessionID, []byte(body), headers)
	if err != nil {
		return 0, nil, fmt.Errorf("tools/list failed: %w", err)
	}
	defer resp.Body.Close()

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

func mcpCallTool(url, sessionID, toolName string, args map[string]any, headers map[string]string) (int, []toolContent, error) {
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

	resp, err := mcpPost(url, sessionID, body, headers)
	if err != nil {
		return 0, nil, fmt.Errorf("tools/call failed: %w", err)
	}
	defer resp.Body.Close()

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

// mcpToolsCallRaw posts tools/call and returns HTTP status and raw body (no JSON-RPC parsing).
func mcpToolsCallRaw(url, sessionID, toolName string, args map[string]any, headers map[string]string) (int, string, error) {
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
		return 0, "", fmt.Errorf("failed to marshal tools/call: %w", err)
	}

	resp, err := mcpPost(url, sessionID, body, headers)
	if err != nil {
		return 0, "", fmt.Errorf("tools/call failed: %w", err)
	}
	defer resp.Body.Close()

	rawBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("read body: %w", err)
	}
	return resp.StatusCode, string(rawBody), nil
}

// parseToolCallGatewayTimeoutError extracts JSON-RPC error -32001 from a tools/call response
// body (SSE event or plain JSON). Expects HTTP 200 from the gateway timeout substitution path.
func parseToolCallGatewayTimeoutError(httpStatus int, body string) (code int, timeoutMs int64, tool string, err error) {
	if httpStatus != http.StatusOK {
		return 0, 0, "", fmt.Errorf("unexpected HTTP status %d: %s", httpStatus, body)
	}
	payload := strings.TrimSpace(body)
	if p, sseErr := extractFirstSSEDataLine(payload); sseErr == nil {
		payload = p
	}
	var msg struct {
		Error *struct {
			Code    int `json:"code"`
			Message string `json:"message"`
			Data    struct {
				TimeoutMS int64  `json:"timeoutMs"`
				Tool      string `json:"tool"`
			} `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(payload), &msg); err != nil {
		return 0, 0, "", fmt.Errorf("parse JSON-RPC: %w (payload %q)", err, payload)
	}
	if msg.Error == nil {
		return 0, 0, "", fmt.Errorf("no JSON-RPC error in response: %s", body)
	}
	return msg.Error.Code, msg.Error.Data.TimeoutMS, msg.Error.Data.Tool, nil
}

func extractFirstSSEDataLine(body string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			return strings.TrimPrefix(line, "data: "), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no SSE data line in body")
}

func mcpRawPost(url, sessionID string, body []byte, headers map[string]string) (int, string, http.Header, error) {
	resp, err := mcpPost(url, sessionID, body, headers)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close()
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

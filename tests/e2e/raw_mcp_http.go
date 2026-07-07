//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
)

var jsonRPCID atomic.Int64

// raw MCP HTTP helpers provide direct control over the Mcp-Session-Id header,
// which the SDK clients abstract away. This is needed for tests that verify
// session affinity across multiple requests (e.g. redis-backed session routing).

var mcpHTTPClient *http.Client

func getMCPHTTPClient() *http.Client {
	if mcpHTTPClient == nil {
		if hc := e2eHTTPClient(gatewayURL); hc != nil {
			mcpHTTPClient = hc
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

// mcpListNames posts a JSON-RPC list request (e.g. tools/list, prompts/list)
// and extracts the name strings from the given result key.
func mcpListNames(ctx context.Context, url, sessionID, method, resultKey string, headers map[string]string) (int, []string, error) {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"%s"}`, jsonRPCID.Add(1), method)
	resp, err := mcpPost(ctx, url, sessionID, []byte(body), headers)
	if err != nil {
		return 0, nil, fmt.Errorf("%s failed: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return resp.StatusCode, nil, fmt.Errorf("%s returned status %d (body unreadable: %w)", method, resp.StatusCode, readErr)
		}
		return resp.StatusCode, nil, fmt.Errorf("%s returned status %d: %s", method, resp.StatusCode, string(respBody))
	}

	result, err := readJSONRPCResult(resp)
	if err != nil {
		return resp.StatusCode, nil, err
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(result, &raw); err != nil {
		return resp.StatusCode, nil, fmt.Errorf("failed to parse %s result: %w: %s", method, err, string(result))
	}
	itemsJSON, ok := raw[resultKey]
	if !ok {
		return resp.StatusCode, nil, fmt.Errorf("key %q not found in %s result: %s", resultKey, method, string(result))
	}
	var items []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(itemsJSON, &items); err != nil {
		return resp.StatusCode, nil, fmt.Errorf("failed to parse %s[%s]: %w: %s", method, resultKey, err, string(itemsJSON))
	}
	names := make([]string, len(items))
	for i, item := range items {
		names[i] = item.Name
	}
	return resp.StatusCode, names, nil
}

func mcpListTools(ctx context.Context, url, sessionID string, headers map[string]string) (int, []string, error) {
	return mcpListNames(ctx, url, sessionID, "tools/list", "tools", headers)
}

func mcpCallTool(ctx context.Context, url, sessionID, toolName string, args map[string]any, headers map[string]string) (int, []toolContent, error) {
	params := map[string]any{"name": toolName}
	if len(args) > 0 {
		params["arguments"] = args
	}
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      jsonRPCID.Add(1),
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

func mcpListPrompts(ctx context.Context, url, sessionID string, headers map[string]string) (int, []string, error) {
	return mcpListNames(ctx, url, sessionID, "prompts/list", "prompts", headers)
}

func mcpGetPrompt(ctx context.Context, url, sessionID, promptName string, args map[string]string, headers map[string]string) (int, string, error) {
	params := map[string]any{"name": promptName}
	if len(args) > 0 {
		params["arguments"] = args
	}
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      jsonRPCID.Add(1),
		"method":  "prompts/get",
		"params":  params,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, "", fmt.Errorf("failed to marshal prompts/get: %w", err)
	}

	resp, err := mcpPost(ctx, url, sessionID, body, headers)
	if err != nil {
		return 0, "", fmt.Errorf("prompts/get failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", fmt.Errorf("reading prompts/get response: %w", err)
	}
	return resp.StatusCode, string(respBody), nil
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

// discoverToolsResponse mirrors the broker's discover_tools output
type discoverToolsResponse struct {
	Servers []discoverServerInfo `json:"servers"`
}

type discoverServerInfo struct {
	Name       string   `json:"name"`
	Categories []string `json:"categories"`
	Hint       string   `json:"hint,omitempty"`
	Tools      []string `json:"tools"`
}

// mcpCallDiscoverTools calls discover_tools via tools/call and parses the response
func mcpCallDiscoverTools(ctx context.Context, url, sessionID string, args map[string]any, headers map[string]string) (int, *discoverToolsResponse, error) { //nolint:unparam
	status, content, err := mcpCallTool(ctx, url, sessionID, "discover_tools", args, headers)
	if err != nil {
		return status, nil, err
	}
	if len(content) == 0 {
		return status, nil, fmt.Errorf("discover_tools returned no content")
	}
	var resp discoverToolsResponse
	if err := json.Unmarshal([]byte(content[0].Text), &resp); err != nil {
		return status, nil, fmt.Errorf("failed to parse discover_tools response: %w: %s", err, content[0].Text)
	}
	return status, &resp, nil
}

// selectToolsResult mirrors the broker's select_tools output
type selectToolsResult struct {
	Status  string   `json:"status"`
	Tools   []string `json:"tools,omitempty"`
	Warning string   `json:"warning,omitempty"`
}

// mcpCallSelectTools calls select_tools via tools/call and parses the response
func mcpCallSelectTools(ctx context.Context, url, sessionID string, tools []string, headers map[string]string) (int, *selectToolsResult, error) { //nolint:unparam
	args := map[string]any{"tools": toAnySlice(tools)}
	status, content, err := mcpCallTool(ctx, url, sessionID, "select_tools", args, headers)
	if err != nil {
		return status, nil, err
	}
	if len(content) == 0 {
		return status, nil, fmt.Errorf("select_tools returned no content")
	}
	var resp selectToolsResult
	if err := json.Unmarshal([]byte(content[0].Text), &resp); err != nil {
		return status, nil, fmt.Errorf("failed to parse select_tools response: %w: %s", err, content[0].Text)
	}
	return status, &resp, nil
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// readJSONRPCResult reads a JSON-RPC result from an HTTP response, handling both JSON and SSE content types.
// It checks Content-Type first, then falls back to content sniffing if JSON parsing fails.
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
		// content-type was not SSE but body looks like SSE (e.g. ext_proc immediate response)
		if looksLikeSSE(rawBody) {
			return parseSSEResult(rawBody)
		}
		return nil, fmt.Errorf("failed to parse JSON response: %w: %s", err, string(rawBody))
	}
	if msg.Error != nil {
		return nil, fmt.Errorf("JSON-RPC error: %s", string(msg.Error))
	}
	return msg.Result, nil
}

// looksLikeSSE returns true if the body starts with an SSE field prefix
func looksLikeSSE(body []byte) bool {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	return bytes.HasPrefix(trimmed, []byte("event:")) || bytes.HasPrefix(trimmed, []byte("data:"))
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
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &msg) == nil {
			if msg.Error != nil {
				return nil, fmt.Errorf("JSON-RPC error: %s", string(msg.Error))
			}
			if msg.Result != nil {
				return msg.Result, nil
			}
		}
	}
	return nil, fmt.Errorf("no result found in SSE response: %s", string(body))
}

// mcpInitializeWithElicitation sends an initialize with elicitation capability declared
func mcpInitializeWithElicitation(mcpURL string, headers map[string]string) (string, error) {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{"elicitation":{}},"clientInfo":{"name":"e2e-raw-elicit","version":"0.0.1"}}}`
	resp, err := mcpPost(context.Background(), mcpURL, "", []byte(body), headers)
	if err != nil {
		return "", fmt.Errorf("initialize request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
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

// mcpCallToolRaw calls a tool and returns the raw SSE body without parsing the result.
// This is needed for tests that expect -32042 errors which readJSONRPCResult treats as failures.
func mcpCallToolRaw(mcpURL, sessionID, toolName string, args map[string]any, headers map[string]string) (int, string, http.Header, error) { //nolint:unparam
	params := map[string]any{"name": toolName}
	if len(args) > 0 {
		params["arguments"] = args
	}
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      jsonRPCID.Add(1),
		"method":  "tools/call",
		"params":  params,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, "", nil, fmt.Errorf("failed to marshal tools/call: %w", err)
	}
	return mcpRawPost(context.Background(), mcpURL, sessionID, body, headers)
}

// sseError represents a JSON-RPC error from an SSE data line
type sseError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// parseSSEError extracts a JSON-RPC error from an SSE response body.
// Returns the error code, message, and the data field (which may contain a URL for -32042).
func parseSSEError(body string) (*sseError, error) {
	scanner := bufio.NewScanner(strings.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var msg struct {
			Error *sseError `json:"error"`
		}
		if json.Unmarshal([]byte(data), &msg) == nil && msg.Error != nil {
			return msg.Error, nil
		}
	}
	return nil, fmt.Errorf("no error found in SSE response: %s", body)
}

// extractElicitationURL parses the URL from a -32042 error's data.elicitations array
func extractElicitationURL(sseErr *sseError) (string, error) {
	var data struct {
		Elicitations []struct {
			URL string `json:"url"`
		} `json:"elicitations"`
	}
	if err := json.Unmarshal(sseErr.Data, &data); err != nil {
		return "", fmt.Errorf("failed to parse elicitation data: %w", err)
	}
	if len(data.Elicitations) == 0 || data.Elicitations[0].URL == "" {
		return "", fmt.Errorf("no url in elicitation error data")
	}
	return data.Elicitations[0].URL, nil
}

// extractHiddenField extracts the value of a hidden input field from HTML
func extractHiddenField(html, fieldName string) string {
	re := regexp.MustCompile(`<input[^>]+name="` + regexp.QuoteMeta(fieldName) + `"[^>]+value="([^"]*)"`)
	m := re.FindStringSubmatch(html)
	if len(m) < 2 {
		re = regexp.MustCompile(`<input[^>]+value="([^"]*)"[^>]+name="` + regexp.QuoteMeta(fieldName) + `"`)
		m = re.FindStringSubmatch(html)
	}
	if len(m) >= 2 {
		return m[1]
	}
	return ""
}

// adaptElicitationURL rewrites the elicitation URL from the public-facing HTTPS host
// to the test-reachable gateway base URL. The elicitation_id parameter is preserved.
func adaptElicitationURL(elicitationURL, gatewayBaseURL string) (string, error) {
	parsed, err := url.Parse(elicitationURL)
	if err != nil {
		return "", fmt.Errorf("failed to parse elicitation URL: %w", err)
	}
	elicitationID := parsed.Query().Get("elicitation_id")
	if elicitationID == "" {
		return "", fmt.Errorf("no elicitation_id in URL: %s", elicitationURL)
	}
	base := strings.TrimSuffix(gatewayBaseURL, "/mcp")
	return base + "/tokens?elicitation_id=" + elicitationID, nil
}

// rawHTTPGetFull performs a GET and returns status, body, and response cookies
func rawHTTPGetFull(targetURL string, headers map[string]string) (int, string, []*http.Cookie, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, "", nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := getMCPHTTPClient().Do(req)
	if err != nil {
		return 0, "", nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", nil, err
	}
	return resp.StatusCode, string(body), resp.Cookies(), nil
}

// rawHTTPPostForm performs a POST with form-encoded values
func rawHTTPPostForm(targetURL string, values url.Values, headers map[string]string, cookies ...*http.Cookie) (int, string, error) { //nolint:unparam
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, targetURL, strings.NewReader(values.Encode()))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := getMCPHTTPClient().Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}
	return resp.StatusCode, string(body), nil
}

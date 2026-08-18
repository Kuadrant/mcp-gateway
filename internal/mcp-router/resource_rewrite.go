package mcprouter

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/url"

	"github.com/Kuadrant/mcp-gateway/internal/routing"
)

// resourceURIRewriter rewrites _meta.ui.resourceUri in tools/call responses so the
// URI a client gets back from a tool call matches the prefixed form resources/list
// already returns for the same resource.
//
// It follows the same incremental Process/Flush line-buffering discipline as
// elicitationRewriter (split on '\n', hold the trailing partial line for the next
// chunk) rather than buffering the whole body until Flush. That matters because
// resourceURIRewriter runs chained after elicitationRewriter on the same response:
// when both are active (a tool call to a resource-federated server from an
// elicitation-capable client), elicitationRewriter must forward "elicitation/create"
// immediately so the client can answer it while the backend call is still open. A
// rewriter further down the chain that holds every byte until end-of-stream would
// swallow that message and deadlock the whole exchange. Rewriting per complete line
// as it arrives avoids that, and still handles both body shapes a tools/call
// response can arrive in - SSE "data: {...}" lines, or a single-line plain JSON
// object with no framing at all.
type resourceURIRewriter struct {
	buf    []byte
	prefix string
	logger *slog.Logger
}

// Process receives a chunk of response data and rewrites any complete lines it
// contains. Splitting on '\n' ensures only fully received JSON is parsed and
// rewritten; an incomplete trailing line is held for the next chunk (or Flush).
func (r *resourceURIRewriter) Process(ctx context.Context, chunk []byte) []byte {
	r.buf = append(r.buf, chunk...)

	var output []byte
	for {
		idx := bytes.IndexByte(r.buf, '\n')
		if idx == -1 {
			break // no complete line - hold remainder for next chunk
		}

		line := r.buf[:idx+1] // include '\n'
		r.buf = r.buf[idx+1:]

		output = append(output, r.maybeRewriteLine(ctx, line)...)
	}

	return output
}

// Flush rewrites and returns any remaining buffered (incomplete-line) data. Safe
// to call multiple times; subsequent calls are no-ops since the buffer is cleared
// after the first call.
func (r *resourceURIRewriter) Flush(ctx context.Context) []byte {
	remaining := r.buf
	r.buf = nil
	if len(remaining) == 0 {
		return remaining
	}
	return r.maybeRewriteLine(ctx, remaining)
}

// maybeRewriteLine rewrites a single line if it contains a tools/call result with
// a ui:// _meta.ui.resourceUri, preserving the original line (including its '\n'
// and any "data: " SSE prefix) exactly when there's nothing to rewrite.
func (r *resourceURIRewriter) maybeRewriteLine(ctx context.Context, line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return line
	}

	jsonData := trimmed
	hasDataPrefix := bytes.HasPrefix(trimmed, dataPrefix)
	if hasDataPrefix {
		jsonData = bytes.TrimSpace(bytes.TrimPrefix(trimmed, dataPrefix))
	}
	if len(jsonData) == 0 || jsonData[0] != '{' {
		return line // not a JSON object on this line (event:, id:, blank, etc.) - leave untouched
	}

	rewritten, changed := r.rewriteToolResultJSON(ctx, jsonData)
	if !changed {
		return line
	}

	hasNewline := bytes.HasSuffix(line, []byte("\n"))
	if hasDataPrefix {
		rewritten = append([]byte("data: "), rewritten...)
	}
	if hasNewline {
		rewritten = append(rewritten, '\n')
	}
	return rewritten
}

// rewriteToolResultJSON rewrites _meta.ui.resourceUri in a single JSON-RPC message
// if present. Every level of the message is round-tripped through json.RawMessage
// so only the resourceUri value itself is touched - all other fields, including
// unrelated _meta keys, pass through exactly as received.
func (r *resourceURIRewriter) rewriteToolResultJSON(ctx context.Context, jsonData []byte) (rewritten []byte, changed bool) {
	var msg jsonRPCMessage
	if err := json.Unmarshal(jsonData, &msg); err != nil {
		return jsonData, false
	}
	if msg.Method != "" || len(msg.Result) == 0 {
		return jsonData, false // not a tool call result (requests/notifications have Method; errors have no Result)
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		return jsonData, false
	}
	metaRaw, ok := result["_meta"]
	if !ok {
		return jsonData, false
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return jsonData, false
	}
	uiRaw, ok := meta["ui"]
	if !ok {
		return jsonData, false
	}
	var ui map[string]json.RawMessage
	if err := json.Unmarshal(uiRaw, &ui); err != nil {
		return jsonData, false
	}
	uriRaw, ok := ui["resourceUri"]
	if !ok {
		return jsonData, false
	}
	var uri string
	if err := json.Unmarshal(uriRaw, &uri); err != nil {
		return jsonData, false
	}

	newURI, ok := injectResourcePrefix(uri, r.prefix)
	if !ok {
		return jsonData, false // not a ui:// URI - left untouched per design
	}

	newURIBytes, err := json.Marshal(newURI)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to marshal rewritten resource URI", "error", err)
		return jsonData, false
	}
	ui["resourceUri"] = newURIBytes

	uiBytes, err := json.Marshal(ui)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to marshal rewritten ui meta", "error", err)
		return jsonData, false
	}
	meta["ui"] = uiBytes

	metaBytes, err := json.Marshal(meta)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to marshal rewritten meta", "error", err)
		return jsonData, false
	}
	result["_meta"] = metaBytes

	resultBytes, err := json.Marshal(result)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to marshal rewritten result", "error", err)
		return jsonData, false
	}
	msg.Result = resultBytes

	out, err := json.Marshal(&msg)
	if err != nil {
		r.logger.ErrorContext(ctx, "failed to marshal rewritten tool result", "error", err)
		return jsonData, false
	}
	return out, true
}

// injectResourcePrefix injects the server prefix into a ui:// URI's authority
// segment (ui://template.html -> ui://<prefix_>template.html), mirroring
// broker.go's rewriteResourceURI so resources/list and tools/call responses stay
// consistent. Non-ui:// or malformed URIs are returned unchanged with ok=false.
func injectResourcePrefix(uri, prefix string) (rewritten string, ok bool) {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "ui" {
		return uri, false
	}
	u.User = nil // never forward upstream credentials to clients
	u.Host = routing.EnsureSeparator(prefix) + u.Host
	return u.String(), true
}

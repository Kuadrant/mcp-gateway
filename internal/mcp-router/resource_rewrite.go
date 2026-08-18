package mcprouter

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Kuadrant/mcp-gateway/internal/routing"
)

// maxBufferedLineBytes caps how much of an unterminated line Process will hold
// waiting for '\n'. Past this, the line is forwarded unrewritten rather than
// buffered indefinitely - bounds memory against a body with a pathologically
// long or missing line terminator.
const maxBufferedLineBytes = 1 << 20 // 1 MiB

// resourceURIRewriter rewrites _meta.ui.resourceUri in tools/call responses to
// match the prefixed form resources/list already returns for the same resource.
// Rewrites incrementally per complete line, never buffering a full line past
// Flush, so it composes safely with elicitationRewriter on the same response.
type resourceURIRewriter struct {
	buf        []byte
	overflowed bool // true while forwarding an abandoned oversized line unrewritten, until its '\n'
	prefix     string
	logger     *slog.Logger
}

// Process receives a chunk of response data and rewrites any complete lines it
// contains. Splitting on '\n' ensures only fully received JSON is parsed and
// rewritten; an incomplete trailing line is held for the next chunk (or Flush),
// unless it exceeds maxBufferedLineBytes, in which case it's forwarded unrewritten.
func (r *resourceURIRewriter) Process(ctx context.Context, chunk []byte) []byte {
	var output []byte

	if r.overflowed {
		idx := bytes.IndexByte(chunk, '\n')
		if idx == -1 {
			return chunk // still inside the abandoned line - keep passing through
		}
		output = append(output, chunk[:idx+1]...)
		chunk = chunk[idx+1:]
		r.overflowed = false
	}

	r.buf = append(r.buf, chunk...)

	for {
		idx := bytes.IndexByte(r.buf, '\n')
		if idx == -1 {
			if len(r.buf) > maxBufferedLineBytes {
				output = append(output, r.buf...)
				r.buf = nil
				r.overflowed = true
			}
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

	newURI, ok := routing.InjectResourcePrefix(uri, r.prefix)
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

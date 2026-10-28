package mcprouter

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Kuadrant/mcp-gateway/internal/idmap"
	"github.com/Kuadrant/mcp-gateway/internal/routing"
)

// elicitationRewriter rewrites sse elicitation requests based on contents of idMap
// idMap entries are managed in the following way:
//  1. Client calls tool
//  2. Backend starts streaming response
//  3. backend sends elicitation/create in the stream - stream stays open - id stored
//  4. Client sends elicitation response (separate HTTP request) -> Lookup() reads the entry,
//     Remove() is called after the response is successfully forwarded
//  5. Backend receives the response, continues processing, sends the tool result
//  6. Stream ends -> Flush() called -> Remove() is called on entries to clean up any orphaned elicitations,
//     is noop for already removed keys
type elicitationRewriter struct {
	lines      sseLineReader
	idMap      idmap.Map
	req        *routing.MCPRequest
	logger     *slog.Logger
	gatewayIDs []string
}

// Process receives a chunk of SSE response data and rewrites any
// elicitation/create request IDs. Only complete lines are parsed, so only
// fully received JSON-RPC messages are rewritten.
func (w *elicitationRewriter) Process(ctx context.Context, chunk []byte) []byte {
	w.lines.Write(chunk)

	var output []byte
	for {
		raw, line, ok := w.lines.Next()
		if !ok {
			break // no complete line - hold remainder for next chunk
		}
		output = append(output, w.maybeRewriteElicitation(ctx, raw, line)...)
	}
	return output
}

// Flush flushes the buffer and cleans up any gatewayIDs created by the rewriter
// This allows us to deal with orphaned elicitation id mappings
// Safe to call multiple times; subsequent calls are no-ops
func (w *elicitationRewriter) Flush(ctx context.Context) []byte {
	remaining := w.lines.Pending()
	w.lines.Reset()
	if len(remaining) > 0 {
		remaining = w.maybeRewriteElicitation(ctx, remaining, remaining)
	}
	for _, id := range w.gatewayIDs {
		w.idMap.Remove(ctx, id) // tool request + response finished, no need to hold onto the mappings any more
	}
	w.gatewayIDs = nil
	return remaining
}

type jsonRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method,omitempty"`
	ID      any             `json:"id,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// maybeRewriteElicitation returns raw unchanged unless line (raw without its
// terminator) is a data field carrying an elicitation/create request.
func (w *elicitationRewriter) maybeRewriteElicitation(ctx context.Context, raw, line []byte) []byte {
	jsonData, ok := sseDataValue(line)
	if !ok {
		return raw
	}

	var msg jsonRPCMessage
	if err := json.Unmarshal(jsonData, &msg); err != nil {
		return raw // not jsonrpc, so definitely not an elicitation req to rewrite
	}

	if msg.Method != "elicitation/create" || msg.ID == nil {
		return raw
	}

	gatewayID, err := w.idMap.Store(ctx, msg.ID, w.req.ServerName, w.req.BackendSessionID, w.req.GetSessionID())
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to store elicitation mapping", "error", err)
		return raw
	}
	w.logger.DebugContext(
		ctx,
		"rewriting elicitation request ID",
		"backendID",
		msg.ID,
		"gatewayID",
		gatewayID,
		"serverName",
		w.req.ServerName,
	)

	w.gatewayIDs = append(w.gatewayIDs, gatewayID)

	msg.ID = gatewayID
	rewritten, err := json.Marshal(&msg)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to marshal rewritten elicitation", "error", err)
		return raw
	}

	// preserve original line ending
	return append(append(append(dataPrefix, ' '), rewritten...), raw[len(line):]...)
}

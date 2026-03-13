package mcprouter

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Kuadrant/mcp-gateway/internal/idmap"
)

var dataPrefix = []byte("data:")

// sseRewriter rewrites sse elicitation requests based on contents of idMap
// idMap entries are managed in the following way:
//  1. Client calls tool
//  2. Backend starts streaming response
//  3. backend sends elicitation/create in the stream - stream stays open - id stored
//  4. Client sends elicitation response (separate HTTP request) -> Lookup() reads the entry,
//     Remove() is called after the response is successfully forwarded
//  5. Backend receives the response, continues processing, sends the tool result
//  6. Stream ends -> Flush() called -> Remove() is called on entries to clean up any orphaned elicitations,
//     is noop for already removed keys
type sseRewriter struct {
	buf        []byte
	idMap      idmap.Map
	req        *MCPRequest
	logger     *slog.Logger
	gatewayIDs []string
}

// Process receives a chunk of SSE response data and rewrites any elicitation/create request IDs.
// As SSE is a line-based protocol, splitting on \n ensures we only
// parse and rewrite fully received JSON-RPC messages
func (w *sseRewriter) Process(ctx context.Context, chunk []byte) []byte {
	w.buf = append(w.buf, chunk...)

	var output []byte
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx == -1 {
			break // no complete line - hold remainder for next chunk
		}

		line := w.buf[:idx+1] // include \n
		w.buf = w.buf[idx+1:]

		// check if this is a SSE event
		if bytes.HasPrefix(bytes.TrimSpace(line), dataPrefix) {
			line = w.maybeRewriteElicitation(ctx, line)
		}

		output = append(output, line...)
	}

	return output
}

// Flush flushes the buffer and cleans up any gatewayIDs created by the rewriter
// This allows us to deal with orphaned elicitation id mappings
func (w *sseRewriter) Flush(ctx context.Context) []byte {
	remaining := w.buf
	w.buf = nil
	for _, id := range w.gatewayIDs {
		w.idMap.Remove(ctx, id) // tool request + response finished, no need to hold onto the mappings any more
	}
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

func (w *sseRewriter) maybeRewriteElicitation(ctx context.Context, line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	jsonData := bytes.TrimPrefix(trimmed, dataPrefix)
	if len(jsonData) > 0 && jsonData[0] == ' ' {
		jsonData = jsonData[1:]
	}

	var msg jsonRPCMessage
	if err := json.Unmarshal(jsonData, &msg); err != nil {
		return line // not jsonrpc, so definitely not an elicitation req to rewrite
	}

	if msg.Method != "elicitation/create" || msg.ID == nil {
		return line
	}

	gatewayID, err := w.idMap.Store(ctx, msg.ID, w.req.serverName, w.req.backendSessionID, w.req.GetSessionID())
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to store elicitation mapping", "error", err)
		return line
	}
	w.logger.InfoContext(
		ctx,
		"rewriting elicitation request ID",
		"backendID",
		msg.ID,
		"gatewayID",
		gatewayID,
		"serverName",
		w.req.serverName,
	)

	w.gatewayIDs = append(w.gatewayIDs, gatewayID)

	msg.ID = gatewayID
	rewritten, err := json.Marshal(&msg)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to marshal rewritten elicitation", "error", err)
		return line
	}

	// preserve original line prefix and ending
	return append(append(append(dataPrefix, ' '), rewritten...), '\n')
}

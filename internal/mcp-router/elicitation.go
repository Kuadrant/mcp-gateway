package mcprouter

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"

	"github.com/Kuadrant/mcp-gateway/internal/idmap"
)

var dataPrefix = []byte("data: ")

type sseRewriter struct {
	buf        []byte
	idMap      idmap.Map
	req        *MCPRequest
	logger     *slog.Logger
	gatewayIDs []string
}

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

func (w *sseRewriter) Flush() []byte {
	remaining := w.buf
	w.buf = nil
	for _, id := range w.gatewayIDs {
		w.idMap.Remove(id) // tool request + response finished, no need to hold onto the mappings any more
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

	var msg jsonRPCMessage
	if err := json.Unmarshal(jsonData, &msg); err != nil {
		return line // not jsonrpc, so definitely not an elicitation req to rewrite
	}

	if msg.Method != "elicitation/create" || msg.ID == nil {
		return line
	}

	gatewayID := w.idMap.Store(msg.ID, w.req.serverName, w.req.backendSessionID)
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
	return append(append(dataPrefix, rewritten...), '\n')
}

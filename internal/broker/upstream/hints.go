package upstream

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/Kuadrant/mcp-gateway/internal/transport"
)

// ToolHints preserves the *bool annotation fidelity of the raw upstream
// tools/list JSON. the SDK's typed decode collapses readOnlyHint and
// idempotentHint to bare bools, losing "unspecified"; mark3labs kept all
// four hints as pointers and the x-mcp-annotation-hints header depends on
// the distinction. field names deliberately match mark3labs' ToolAnnotation
// so the router's rendering code is unchanged.
type ToolHints struct {
	ReadOnlyHint    *bool
	DestructiveHint *bool
	IdempotentHint  *bool
	OpenWorldHint   *bool
	// Raw is the exact annotations object the upstream sent, nil when the
	// tool declared none.
	Raw json.RawMessage
}

// rawTool is the slice of a tools/list result entry the tee cares about.
type rawTool struct {
	Name        string          `json:"name"`
	Annotations json.RawMessage `json:"annotations"`
}

type rawAnnotations struct {
	ReadOnlyHint    *bool `json:"readOnlyHint"`
	DestructiveHint *bool `json:"destructiveHint"`
	IdempotentHint  *bool `json:"idempotentHint"`
	OpenWorldHint   *bool `json:"openWorldHint"`
}

// toolHintsTee intercepts tools/list exchanges on an upstream HTTP client
// and hands the harvested per-tool hints to sink, keyed by unprefixed tool
// name. every other request passes through untouched, so the tools/call
// fast path never sees it.
type toolHintsTee struct {
	base http.RoundTripper
	sink func(map[string]ToolHints)
}

func (t *toolHintsTee) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || !isToolsListRequest(req) {
		return t.base.RoundTrip(req)
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &hintsTeeBody{
		rc:   resp.Body,
		sse:  strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"),
		sink: t.sink,
	}
	return resp, nil
}

// isToolsListRequest peeks at the request body without consuming it.
func isToolsListRequest(req *http.Request) bool {
	body, ok := transport.PeekRequestBody(req)
	if !ok {
		return false
	}
	var env struct {
		Method string `json:"method"`
	}
	return json.Unmarshal(body, &env) == nil && env.Method == "tools/list"
}

// hintsTeeBody accumulates a tools/list response while the SDK client reads
// it, and parses the hints once the stream ends. bounded by the size of the
// response the client is buffering anyway.
type hintsTeeBody struct {
	rc   io.ReadCloser
	buf  bytes.Buffer
	sse  bool
	sink func(map[string]ToolHints)
	sunk bool
}

func (b *hintsTeeBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 {
		b.buf.Write(p[:n])
	}
	if err == io.EOF {
		b.harvest()
	}
	return n, err
}

func (b *hintsTeeBody) Close() error {
	b.harvest()
	return b.rc.Close()
}

func (b *hintsTeeBody) harvest() {
	if b.sunk {
		return
	}
	b.sunk = true
	if b.sse {
		for _, payload := range ssePayloads(b.buf.Bytes()) {
			if hints, ok := parseToolHints(payload); ok {
				b.sink(hints)
				return
			}
		}
		return
	}
	if hints, ok := parseToolHints(b.buf.Bytes()); ok {
		b.sink(hints)
	}
}

// ssePayloads extracts the data payloads from an SSE stream.
func ssePayloads(stream []byte) [][]byte {
	var payloads [][]byte
	for _, event := range bytes.Split(stream, []byte("\n\n")) {
		if data, _ := transport.SSEEventData(event); len(data) > 0 {
			payloads = append(payloads, bytes.Join(data, []byte("\n")))
		}
	}
	return payloads
}

// parseToolHints extracts per-tool hints from a JSON-RPC tools/list
// response payload. ok is false when the payload is not one.
func parseToolHints(payload []byte) (map[string]ToolHints, bool) {
	var msg struct {
		Result struct {
			Tools []rawTool `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(payload, &msg); err != nil || msg.Result.Tools == nil {
		return nil, false
	}
	hints := make(map[string]ToolHints, len(msg.Result.Tools))
	for i := range msg.Result.Tools {
		t := &msg.Result.Tools[i]
		if t.Name == "" {
			continue
		}
		h := ToolHints{}
		if len(t.Annotations) > 0 && !bytes.Equal(t.Annotations, []byte("null")) {
			var ann rawAnnotations
			if err := json.Unmarshal(t.Annotations, &ann); err == nil {
				h = ToolHints{
					ReadOnlyHint:    ann.ReadOnlyHint,
					DestructiveHint: ann.DestructiveHint,
					IdempotentHint:  ann.IdempotentHint,
					OpenWorldHint:   ann.OpenWorldHint,
					Raw:             t.Annotations,
				}
			}
		}
		hints[t.Name] = h
	}
	return hints, true
}

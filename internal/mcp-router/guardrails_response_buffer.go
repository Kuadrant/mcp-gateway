package mcprouter

import (
	"bytes"
	"context"
	"encoding/json"
)

// guardrailsResponseBuffer holds back the final tool result so guardrails can
// check it first, while letting earlier SSE events (like elicitation
// prompts) through right away so the client isn't stalled. Plain JSON
// responses are held in full until the response ends.
type guardrailsResponseBuffer struct {
	sse         bool
	sseDetected bool // true once the first non-whitespace byte has settled sse's real framing

	unconsumed []byte // bytes not yet forming a complete '\n'-terminated line
	eventBytes []byte // raw bytes of lines in the currently open (undispatched) SSE event

	requestID any
	check     func(ctx context.Context, body []byte) []byte // nil return means allow (forward original)
	done      bool                                          // true once the terminal result has been resolved

	maxBytes  int    // upper bound on buffered bytes; 0 means unbounded
	oversized []byte // forwarded in place of withheld content once maxBytes is exceeded
}

// newGuardrailsResponseBuffer builds a buffer for one tools/call response.
// sse selects per-event withholding (text/event-stream) vs whole-body
// withholding (application/json). check receives the raw bytes of the
// withheld unit (one SSE event, or the whole JSON body) and returns a
// replacement to forward instead, or nil to forward the original unchanged.
func newGuardrailsResponseBuffer(sse bool, requestID any, check func(context.Context, []byte) []byte) *guardrailsResponseBuffer {
	return &guardrailsResponseBuffer{sse: sse, requestID: requestID, check: check}
}

// withLimit caps how many bytes we'll buffer (0 means no cap). Once a
// response grows past that limit we stop buffering and send oversized
// instead, so a broken or malicious upstream can't use unlimited memory.
func (g *guardrailsResponseBuffer) withLimit(maxBytes int, oversized []byte) *guardrailsResponseBuffer {
	g.maxBytes = maxBytes
	g.oversized = oversized
	return g
}

// Process appends chunk and returns any bytes now safe to forward.
func (g *guardrailsResponseBuffer) Process(ctx context.Context, chunk []byte) []byte {
	if g.done {
		return chunk
	}

	g.detectRawJSON(chunk)
	g.unconsumed = append(g.unconsumed, chunk...)

	if !g.sse {
		// application/json, or an SSE-declared response whose body is
		// actually a bare JSON-RPC document (see detectRawJSON): nothing is
		// safe to forward before the full body is known - hold everything
		// until Flush.
		if g.overLimit() {
			return g.reject()
		}
		return nil
	}

	if g.overLimit() {
		return g.reject()
	}

	var output []byte
	for {
		idx := bytes.IndexByte(g.unconsumed, '\n')
		if idx == -1 {
			break // no complete line yet - hold remainder for next chunk
		}
		line := g.unconsumed[:idx+1]
		g.unconsumed = g.unconsumed[idx+1:]

		if len(bytes.TrimSpace(line)) != 0 {
			g.eventBytes = append(g.eventBytes, line...)
			if g.overLimit() {
				output = append(output, g.reject()...)
				break
			}
			continue
		}

		// blank line: the event assembled so far is complete and dispatched
		event := append(g.eventBytes, line...)
		g.eventBytes = nil

		respID, ok := g.isTerminalResult(event)
		if !ok {
			output = append(output, event...)
			continue
		}

		event = g.normalizeID(event, respID)
		output = append(output, g.resolve(ctx, event)...)
		g.done = true
		output = append(output, g.unconsumed...)
		g.unconsumed = nil
		break
	}
	return output
}

// Flush returns any bytes still withheld when the stream ends. Safe to call
// multiple times; subsequent calls are no-ops.
func (g *guardrailsResponseBuffer) Flush(ctx context.Context) []byte {
	if g.done {
		out := g.unconsumed
		g.unconsumed = nil
		return out
	}
	if g.overLimit() {
		return g.reject()
	}
	g.done = true
	remaining := append(g.eventBytes, g.unconsumed...)
	g.eventBytes = nil
	g.unconsumed = nil
	if len(remaining) == 0 {
		return nil
	}
	return g.resolve(ctx, remaining)
}

// detectRawJSON checks if a response marked as SSE is actually plain JSON
// (starts with '{' instead of SSE syntax) and switches modes if so. It only
// looks at the newest chunk, not everything received so far, so it stays
// fast even if data arrives one byte at a time.
func (g *guardrailsResponseBuffer) detectRawJSON(chunk []byte) {
	if !g.sse || g.sseDetected {
		return
	}
	trimmed := bytes.TrimLeft(chunk, " \t\r\n")
	if len(trimmed) == 0 {
		return // still all whitespace so far
	}
	g.sseDetected = true
	if trimmed[0] == '{' {
		g.sse = false
	}
}

// overLimit reports whether currently buffered bytes exceed maxBytes.
func (g *guardrailsResponseBuffer) overLimit() bool {
	return g.maxBytes > 0 && len(g.unconsumed)+len(g.eventBytes) > g.maxBytes
}

// reject discards all buffered bytes and returns oversized in their place,
// marking the buffer done so any further chunks in this response stream pass
// through unchecked rather than continuing to accumulate.
func (g *guardrailsResponseBuffer) reject() []byte {
	g.done = true
	g.unconsumed = nil
	g.eventBytes = nil
	return g.oversized
}

// resolve runs the guardrails check on body and returns the replacement if
// any, otherwise the original body unchanged.
func (g *guardrailsResponseBuffer) resolve(ctx context.Context, body []byte) []byte {
	if replacement := g.check(ctx, body); replacement != nil {
		return replacement
	}
	return body
}

// isTerminalResult reports whether event is the actual tool result (a
// response), rather than a request or notification, and returns its id.
// Even a missing or mismatched id doesn't skip the check, since this stream
// only ever carries this one client's request.
func (g *guardrailsResponseBuffer) isTerminalResult(event []byte) (id any, ok bool) {
	data := sseEventData(event)
	if len(data) == 0 {
		return nil, false
	}
	var msg jsonRPCMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, false
	}
	if msg.Method != "" {
		return nil, false // request or notification, not a response
	}
	if len(msg.Result) == 0 && len(msg.Error) == 0 {
		return nil, false
	}
	return msg.ID, true
}

// normalizeID rewrites event's JSON-RPC id to g.requestID when it doesn't
// already match id - the client is waiting on a response to its own
// original request id, so a missing, mismatched, or differently-typed
// upstream id must not reach it uncorrected.
func (g *guardrailsResponseBuffer) normalizeID(event []byte, id any) []byte {
	if idsEqual(id, g.requestID) {
		return event
	}
	data := sseEventData(event)
	var msg jsonRPCMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return event // unreachable: isTerminalResult already parsed this event
	}
	msg.ID = g.requestID
	rewritten, err := json.Marshal(&msg)
	if err != nil {
		return event
	}
	return append(append([]byte("event: message\ndata: "), rewritten...), '\n', '\n')
}

// sseEventData reconstructs the data field of one SSE event from its raw
// bytes (all lines up to and including the terminating blank line). Per the
// SSE spec, multiple data: lines within one event are joined with '\n' to
// form the field's value - required here because isTerminalResult must
// parse the whole payload to read id/method/result, not just the first
// data: line.
func sseEventData(event []byte) []byte {
	var parts [][]byte
	remaining := event
	for len(remaining) > 0 {
		idx := bytes.IndexByte(remaining, '\n')
		var line []byte
		if idx == -1 {
			line = remaining
			remaining = nil
		} else {
			line = remaining[:idx]
			remaining = remaining[idx+1:]
		}
		trimmed := bytes.TrimSpace(line)
		if !bytes.HasPrefix(trimmed, dataPrefix) {
			continue
		}
		data := bytes.TrimPrefix(trimmed, dataPrefix)
		if len(data) > 0 && data[0] == ' ' {
			data = data[1:]
		}
		parts = append(parts, data)
	}
	if len(parts) == 0 {
		return nil
	}
	return bytes.Join(parts, []byte("\n"))
}

// idsEqual compares two JSON-RPC ids decoded via `any`. Both sides are
// re-marshaled to JSON so a numeric id decoded as float64 on one side and as
// e.g. json.Number or int on the other still compares correctly.
func idsEqual(a, b any) bool {
	if a == nil || b == nil {
		return false
	}
	ab, errA := json.Marshal(a)
	bb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}

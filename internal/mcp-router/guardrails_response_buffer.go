package mcprouter

import (
	"bytes"
	"context"
	"encoding/json"
)

// guardrailsResponseBuffer holds back the final tool result so guardrails can
// check it first, while letting earlier SSE events (like elicitation
// prompts) through right away so the client isn't stalled. Plain JSON
// responses are held in full until the response ends. Malformed SSE framing
// (a bare JSON body, or a stream ending mid-event) is dropped unchecked.
type guardrailsResponseBuffer struct {
	sse    bool
	events sseEventReader // sse only
	body   []byte         // json only: withheld until Flush

	requestID any
	check     func(ctx context.Context, body []byte) []byte // nil return means allow (forward original)
	done      bool                                          // true once the response is resolved or dropped

	maxBytes  int    // upper bound on buffered bytes; 0 means unbounded
	oversized []byte // forwarded in place of withheld content once maxBytes is exceeded
}

// newGuardrailsResponseBuffer builds a buffer for one tools/call response.
// sse selects per-event withholding (text/event-stream) vs whole-body
// withholding (application/json). check receives the withheld unit (one
// LF-framed SSE event, or the whole JSON body) and returns a replacement to
// forward instead, or nil to forward the original unchanged.
func newGuardrailsResponseBuffer(sse bool, requestID any, check func(context.Context, []byte) []byte) *guardrailsResponseBuffer {
	return &guardrailsResponseBuffer{sse: sse, requestID: requestID, check: check}
}

// withLimit caps how many bytes we'll buffer (0 means no cap): per SSE
// event, or for the whole body when withholding JSON. Once that grows past
// the limit we stop buffering and send oversized instead, so a broken or
// malicious upstream can't use unlimited memory.
func (g *guardrailsResponseBuffer) withLimit(maxBytes int, oversized []byte) *guardrailsResponseBuffer {
	g.maxBytes = maxBytes
	g.oversized = oversized
	return g
}

// Process appends chunk and returns any bytes now safe to forward. Once
// done, chunk is dropped rather than forwarded: a tools/call response holds
// exactly one terminal result, so anything the upstream sends afterward is
// unexpected and must not reach the client without having been through the
// check above - forwarding it unchecked would let a malicious or
// misbehaving upstream slip an unaudited payload past guardrails behind an
// innocuous first result.
func (g *guardrailsResponseBuffer) Process(ctx context.Context, chunk []byte) []byte {
	if g.done {
		return nil
	}

	if !g.sse {
		g.body = append(g.body, chunk...)
		if g.exceeds(len(g.body)) {
			return g.reject()
		}
		return nil
	}

	if err := g.events.Write(chunk); err != nil {
		return g.drop()
	}

	var output []byte
	for {
		event, ok := g.events.Next()
		if !ok {
			break
		}
		if g.exceeds(len(event)) {
			return append(output, g.reject()...)
		}
		respID, ok := g.isTerminalResult(event)
		if !ok {
			output = append(output, event...)
			continue
		}
		output = append(output, g.resolve(ctx, g.normalizeID(event, respID))...)
		g.drop() // anything after the terminal event is unexpected trailing data
		return output
	}
	// the limit applies per event: only the open event and partial line remain
	if g.exceeds(g.events.Buffered()) {
		return append(output, g.reject()...)
	}
	return output
}

// Flush returns any bytes still withheld when the stream ends. Safe to call
// multiple times; subsequent calls are no-ops. An SSE stream that ends
// mid-event has no complete terminal result, so its buffered bytes are
// dropped rather than checked or forwarded.
func (g *guardrailsResponseBuffer) Flush(ctx context.Context) []byte {
	if g.done {
		return nil
	}
	if g.sse {
		return g.drop()
	}
	body := g.body
	g.done, g.body = true, nil
	if len(body) == 0 {
		return nil
	}
	return g.resolve(ctx, body)
}

func (g *guardrailsResponseBuffer) exceeds(n int) bool {
	return g.maxBytes > 0 && n > g.maxBytes
}

// drop discards all buffered bytes and marks the buffer done without
// forwarding anything.
func (g *guardrailsResponseBuffer) drop() []byte {
	g.done = true
	g.body = nil
	g.events = sseEventReader{}
	return nil
}

// reject discards all buffered bytes and returns oversized in their place,
// marking the buffer done so any further chunks in this response stream are
// dropped (see Process's doc comment) rather than continuing to accumulate.
func (g *guardrailsResponseBuffer) reject() []byte {
	g.drop()
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
	// a result or error makes this the response, even if the upstream also
	// sent a (spoofed or malformed) method - gating on method's absence let
	// a crafted event carrying both fields skip the check entirely.
	if len(msg.Result) == 0 && len(msg.Error) == 0 {
		return nil, false // request or notification, not a response
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

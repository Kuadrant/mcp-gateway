package mcprouter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGuardrailsResponseBuffer_SSE_ElicitationForwardedBeforeResult(t *testing.T) {
	// the elicitation/create event must be forwarded as soon as it is
	// received - it must not wait on the tool result, which itself may be
	// waiting on the client's answer to this very elicitation.
	var checked [][]byte
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, body []byte) []byte {
		checked = append(checked, body)
		return nil // allow
	})

	elicitEvent := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":99,\"method\":\"elicitation/create\",\"params\":{}}\n\n")
	out := buf.Process(context.Background(), elicitEvent)
	require.Equal(t, string(elicitEvent), string(out), "elicitation event must forward immediately, unwithheld")
	require.Empty(t, checked, "the elicitation event itself must not be sent to the guardrails checker")

	resultEvent := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}\n\n")
	out = buf.Process(context.Background(), resultEvent)
	require.Equal(t, string(resultEvent), string(out), "allowed result forwards unchanged")
	require.Len(t, checked, 1)
	require.Equal(t, string(resultEvent), string(checked[0]))
}

func TestGuardrailsResponseBuffer_SSE_ResultWithheldUntilComplete(t *testing.T) {
	var checked int
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		checked++
		return nil
	})

	full := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}\n\n")

	// split into two chunks mid-event: nothing should be forwarded or
	// checked until the terminating blank line arrives.
	part1 := full[:20]
	part2 := full[20:]

	out := buf.Process(context.Background(), part1)
	require.Empty(t, out, "no complete event yet")
	require.Zero(t, checked)

	out = buf.Process(context.Background(), part2)
	require.Equal(t, string(full), string(out))
	require.Equal(t, 1, checked)
}

func TestGuardrailsResponseBuffer_SSE_Blocked(t *testing.T) {
	blocked := []byte("\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"blocked\"}],\"isError\":true}}\n\n")
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		return blocked
	})

	resultEvent := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"secret\"}]}}\n\n")
	out := buf.Process(context.Background(), resultEvent)
	require.Equal(t, string(blocked), string(out))
}

func TestGuardrailsResponseBuffer_SSE_NonMatchingIDStillChecked(t *testing.T) {
	// this stream is the response to this client's own tools/call request,
	// so a response-shaped event with a mismatched id is not a legitimate
	// unrelated exchange - it must still be sent to guardrails (never
	// forwarded unchecked), and the id forwarded to the client must be
	// corrected to the client's own request id.
	var checked [][]byte
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, body []byte) []byte {
		checked = append(checked, body)
		return nil // allow
	})

	otherEvent := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"other\"}]}}\n\n")
	out := buf.Process(context.Background(), otherEvent)

	require.Len(t, checked, 1, "a mismatched-id response-shaped event must still be sent to guardrails")
	corrected := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"other\"}]}}\n\n")
	require.Equal(t, string(corrected), string(out), "the forwarded event's id must be corrected to the client's own request id")
}

func TestGuardrailsResponseBuffer_SSE_MissingIDStillChecked(t *testing.T) {
	// a response-shaped event with no id at all must be treated the same as
	// a mismatched id: checked by guardrails, then forwarded with the
	// client's own request id.
	var checked int
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		checked++
		return nil
	})

	noIDEvent := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"other\"}]}}\n\n")
	out := buf.Process(context.Background(), noIDEvent)

	require.Equal(t, 1, checked)
	corrected := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"other\"}]}}\n\n")
	require.Equal(t, string(corrected), string(out))
}

func TestGuardrailsResponseBuffer_SSE_NonMatchingIDStillBlockable(t *testing.T) {
	// guardrails must be able to block/replace a mismatched-id response just
	// like a matching-id one - the id mismatch itself must never grant a
	// free pass around the check.
	blocked := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"blocked\"}],\"isError\":true}}\n\n")
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		return blocked
	})

	otherEvent := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"secret\"}]}}\n\n")
	out := buf.Process(context.Background(), otherEvent)
	require.Equal(t, string(blocked), string(out))
}

func TestGuardrailsResponseBuffer_SSE_MultiLineDataFieldMatched(t *testing.T) {
	// the terminal result JSON may be split across multiple data: lines
	// within one event - matching must reconstruct it before checking the id.
	var checked int
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		checked++
		return nil
	})

	event := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\ndata: \"result\":{\"content\":[{\"type\":\"text\",\"text\":\"joined\"}]}}\n\n")
	out := buf.Process(context.Background(), event)
	require.Equal(t, string(event), string(out))
	require.Equal(t, 1, checked)
}

func TestGuardrailsResponseBuffer_SSE_AfterDoneForwardsUnchanged(t *testing.T) {
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		return nil
	})

	resultEvent := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[]}}\n\n")
	_ = buf.Process(context.Background(), resultEvent)
	require.True(t, buf.done)

	trailer := []byte("trailing bytes after the result")
	out := buf.Process(context.Background(), trailer)
	require.Equal(t, string(trailer), string(out))
}

func TestGuardrailsResponseBuffer_SSE_FlushResolvesUndispatchedTrailer(t *testing.T) {
	// a malformed upstream that never sends the terminating blank line for
	// the final event must still have its buffered content resolved at
	// end-of-stream, rather than being silently dropped.
	var checkedBody []byte
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, body []byte) []byte {
		checkedBody = body
		return nil
	})

	event := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"cut off\"}]}}\n")
	out := buf.Process(context.Background(), event)
	require.Empty(t, out, "no blank line yet - nothing forwarded")

	out = buf.Flush(context.Background())
	require.Equal(t, string(event), string(out))
	require.Equal(t, string(event), string(checkedBody))
}

func TestGuardrailsResponseBuffer_JSON_WithheldUntilFlush(t *testing.T) {
	var checked int
	buf := newGuardrailsResponseBuffer(false, 1, func(_ context.Context, _ []byte) []byte {
		checked++
		return nil
	})

	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`)
	part1 := body[:10]
	part2 := body[10:]

	out := buf.Process(context.Background(), part1)
	require.Nil(t, out, "application/json must not forward anything before end of stream")
	require.Zero(t, checked)

	out = buf.Process(context.Background(), part2)
	require.Nil(t, out)
	require.Zero(t, checked)

	out = buf.Flush(context.Background())
	require.Equal(t, string(body), string(out))
	require.Equal(t, 1, checked)
}

func TestGuardrailsResponseBuffer_JSON_Blocked(t *testing.T) {
	blocked := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"blocked"}],"isError":true}}`)
	buf := newGuardrailsResponseBuffer(false, 1, func(_ context.Context, _ []byte) []byte {
		return blocked
	})

	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"secret"}]}}`)
	_ = buf.Process(context.Background(), body)
	out := buf.Flush(context.Background())
	require.Equal(t, string(blocked), string(out))
}

func TestGuardrailsResponseBuffer_JSON_FlushIsIdempotent(t *testing.T) {
	buf := newGuardrailsResponseBuffer(false, 1, func(_ context.Context, _ []byte) []byte {
		return nil
	})
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`)
	_ = buf.Process(context.Background(), body)

	first := buf.Flush(context.Background())
	require.Equal(t, string(body), string(first))

	second := buf.Flush(context.Background())
	require.Empty(t, second, "second flush must be a no-op")
}

func TestGuardrailsResponseBuffer_SSE_MultipleSpacesAfterDataPrefixStillChecked(t *testing.T) {
	// per the SSE spec, only a single leading space after "data:" is
	// stripped, so "data:  {...}" (two spaces) legitimately leaves one
	// space before '{'. isTerminalResult must not reject this on a raw
	// first-byte check - json.Unmarshal tolerates the leading whitespace.
	var checked int
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		checked++
		return nil
	})

	event := []byte("event: message\ndata:  {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}\n\n")
	out := buf.Process(context.Background(), event)
	require.Equal(t, string(event), string(out))
	require.Equal(t, 1, checked, "extra leading whitespace after data: must not bypass the guardrails check")
}

func TestGuardrailsResponseBuffer_SSE_OverLimitRejected(t *testing.T) {
	// a broken or malicious upstream must not be able to grow the buffer
	// unboundedly while withholding a never-completed event.
	var checked int
	oversized := []byte("oversized replacement")
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		checked++
		return nil
	}).withLimit(16, oversized)

	out := buf.Process(context.Background(), []byte("event: message\ndata: this line alone already exceeds the limit\n"))
	require.Equal(t, string(oversized), string(out))
	require.True(t, buf.done)
	require.Zero(t, checked, "the oversized content itself must never reach the guardrails checker")

	// further chunks in this stream must pass through unchecked, not
	// re-trigger buffering.
	trailer := []byte("more bytes")
	out = buf.Process(context.Background(), trailer)
	require.Equal(t, string(trailer), string(out))
}

func TestGuardrailsResponseBuffer_JSON_OverLimitRejected(t *testing.T) {
	var checked int
	oversized := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"too big"}],"isError":true}}`)
	buf := newGuardrailsResponseBuffer(false, 1, func(_ context.Context, _ []byte) []byte {
		checked++
		return nil
	}).withLimit(8, oversized)

	out := buf.Process(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	require.Equal(t, string(oversized), string(out))
	require.True(t, buf.done)
	require.Zero(t, checked)
}

func TestGuardrailsResponseBuffer_OverLimitAcrossMultipleChunks(t *testing.T) {
	// the limit must apply to cumulative buffered bytes, not just a single
	// Process call - an upstream trickling bytes must not evade it.
	oversized := []byte("oversized replacement")
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		return nil
	}).withLimit(20, oversized)

	out := buf.Process(context.Background(), []byte("event: msg\n"))
	require.Empty(t, out, "11 bytes so far, under the 20 byte limit")
	require.False(t, buf.done)

	out = buf.Process(context.Background(), []byte("data: more bytes than the limit allows\n"))
	require.Equal(t, string(oversized), string(out), "cumulative bytes across both chunks now exceed the limit")
	require.True(t, buf.done)
}

func TestGuardrailsResponseBuffer_SSEDeclaredButRawJSONBody_WithheldInFull(t *testing.T) {
	// content-type can be missing or non-standard (e.g.
	// application/problem+json), which the caller conservatively treats as
	// SSE. If the actual body is a bare JSON-RPC document with no SSE
	// framing, per-event parsing would never find a "data:" line and could
	// forward chunks unchecked whenever they happen to contain a blank line.
	// The buffer must detect this from the body itself and fall back to
	// whole-body withholding.
	var checkedBody []byte
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, body []byte) []byte {
		checkedBody = body
		return nil
	})

	body := []byte("{\n\n\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}")
	out := buf.Process(context.Background(), body)
	require.Empty(t, out, "raw JSON body must be withheld in full, not forwarded piecemeal at the embedded blank line")
	require.Nil(t, checkedBody, "not checked until Flush")

	out = buf.Flush(context.Background())
	require.Equal(t, string(body), string(out))
	require.Equal(t, string(body), string(checkedBody))
}

func TestGuardrailsResponseBuffer_SSEDeclaredButRawJSONBody_DetectedAcrossChunks(t *testing.T) {
	// leading whitespace before the body arrives in its own chunk (with no
	// newline yet, so the SSE per-event loop cannot misread it as a blank
	// line) must not prevent raw-JSON detection once the '{' itself arrives.
	var checked int
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		checked++
		return nil
	})

	out := buf.Process(context.Background(), []byte("   "))
	require.Empty(t, out)

	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`)
	out = buf.Process(context.Background(), body)
	require.Empty(t, out, "still withheld as raw JSON, not parsed as an SSE event")

	out = buf.Flush(context.Background())
	require.Equal(t, 1, checked)
	require.Equal(t, "   "+string(body), string(out))
}

func TestIDsEqual(t *testing.T) {
	require.True(t, idsEqual(float64(1), 1))
	require.True(t, idsEqual("abc", "abc"))
	require.False(t, idsEqual(float64(1), float64(2)))
	require.False(t, idsEqual("abc", "abd"))
	require.False(t, idsEqual(nil, 1))
	require.False(t, idsEqual(1, nil))
	require.False(t, idsEqual(nil, nil))
}

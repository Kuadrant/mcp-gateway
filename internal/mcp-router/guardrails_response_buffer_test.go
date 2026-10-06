package mcprouter

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGuardrailsResponseBuffer_SSE_LineEndings(t *testing.T) {
	tests := []struct {
		name       string
		lineEnding string
	}{
		{name: "LF", lineEnding: "\n"},
		{name: "CRLF", lineEnding: "\r\n"},
		{name: "CR", lineEnding: "\r"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var checked [][]byte
			buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, body []byte) []byte {
				checked = append(checked, body)
				return nil
			})

			elicitData := `{"jsonrpc":"2.0","id":99,"method":"elicitation/create","params":{}}`
			elicitEvent := []byte("event: message" + tc.lineEnding + "data: " + elicitData + tc.lineEnding + tc.lineEnding)
			wantElicit := []byte("event: message\ndata: " + elicitData + "\n\n")

			out := buf.Process(context.Background(), elicitEvent)
			require.Equal(t, string(wantElicit), string(out), "elicitation event must forward immediately")
			require.Empty(t, checked, "elicitation event must not be sent to the guardrails checker")

			resultData := `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`
			resultEvent := []byte("event: message" + tc.lineEnding + "data: " + resultData + tc.lineEnding + tc.lineEnding)
			wantResult := []byte("event: message\ndata: " + resultData + "\n\n")

			out = buf.Process(context.Background(), resultEvent)
			require.Equal(t, string(wantResult), string(out), "allowed result must forward after the check")
			require.Len(t, checked, 1)
		})
	}
}

func TestGuardrailsResponseBuffer_SSE_CRLFCanSplitAcrossEveryChunkBoundary(t *testing.T) {
	var checked int
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		checked++
		return nil
	})

	event := []byte("event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[]}}\r\n\r\n")
	var out []byte
	for _, b := range event {
		out = append(out, buf.Process(context.Background(), []byte{b})...)
	}

	require.Equal(t, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[]}}\n\n", string(out))
	require.Equal(t, 1, checked)
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

func TestGuardrailsResponseBuffer_SSE_AfterDoneDropsTrailingBytes(t *testing.T) {
	// a tools/call response holds exactly one terminal result; anything an
	// upstream sends afterward is unexpected and must be dropped, not
	// forwarded unchecked.
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		return nil
	})

	resultEvent := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[]}}\n\n")
	_ = buf.Process(context.Background(), resultEvent)
	require.True(t, buf.done)

	trailer := []byte("trailing bytes after the result")
	out := buf.Process(context.Background(), trailer)
	require.Empty(t, out, "bytes after the terminal result must be dropped, never forwarded unchecked")
}

func TestGuardrailsResponseBuffer_SSE_UnconsumedAfterTerminalEventInSameChunkDropped(t *testing.T) {
	// a second response-shaped event delivered in the same physical chunk
	// as the terminal result (e.g. a decoy the upstream hopes rides along
	// unchecked) must be dropped along with everything else after done,
	// not appended to the output raw.
	var checked int
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		checked++
		return nil
	})

	terminal := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[]}}\n\n"
	decoy := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"smuggled\"}]}}\n\n"
	out := buf.Process(context.Background(), []byte(terminal+decoy))

	require.True(t, buf.done)
	require.Equal(t, 1, checked, "only the first terminal-shaped event is checked")
	require.NotContains(t, string(out), "smuggled", "a second response-shaped event in the same chunk must not reach the client unchecked")
}

func TestGuardrailsResponseBuffer_SSE_FlushReplacesUndispatchedTrailer(t *testing.T) {
	malformed := []byte("malformed response replacement")
	var checked int
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		checked++
		return nil
	}).withMalformedResponse(malformed)

	progress := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
	partialResult := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"cut off\"}]}}\n")
	out := buf.Process(context.Background(), append(progress, partialResult...))
	require.Equal(t, string(progress), string(out), "complete control events should still be forwarded")
	require.Zero(t, checked, "the incomplete event must not reach guardrails")

	out = buf.Flush(context.Background())
	require.Equal(t, string(malformed), string(out))
	require.Zero(t, checked, "the incomplete event must not reach guardrails")
	require.True(t, buf.done)
	require.Empty(t, buf.Flush(context.Background()), "flush after replacement is a no-op")
}

func TestGuardrailsResponseBuffer_SSE_FlushAtEventBoundaryDoesNotReplace(t *testing.T) {
	malformed := []byte("malformed response replacement")
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		return nil
	}).withMalformedResponse(malformed)

	progress := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n")
	require.Equal(t, string(progress), string(buf.Process(context.Background(), progress)))
	require.Empty(t, buf.Flush(context.Background()), "a complete event boundary does not trigger malformed framing replacement")
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

	// further chunks in this stream must be dropped, not re-trigger
	// buffering and not forwarded unchecked either.
	trailer := []byte("more bytes")
	out = buf.Process(context.Background(), trailer)
	require.Empty(t, out, "bytes after rejection must be dropped, never forwarded unchecked")
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

func TestGuardrailsResponseBuffer_SSE_LimitAppliesPerEventNotPerChunk(t *testing.T) {
	// Envoy may coalesce several events into one chunk; two events each
	// under the limit must pass even when the chunk as a whole exceeds it.
	progress := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n"
	result := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[]}}\n\n"
	limit := max(len(progress), len(result))
	require.Greater(t, len(progress)+len(result), limit)

	var checked int
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		checked++
		return nil
	}).withLimit(limit, []byte("oversized"))

	out := buf.Process(context.Background(), []byte(progress+result))
	require.Equal(t, progress+result, string(out))
	require.Equal(t, 1, checked)
}

func TestGuardrailsResponseBuffer_SSE_PartialLineCountsTowardEventLimit(t *testing.T) {
	// a complete event forwarded earlier in the chunk must not count, but
	// the open event's partial line must.
	progress := "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n"
	oversized := []byte("oversized")
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		return nil
	}).withLimit(len(progress), oversized)

	partial := "data: " + string(bytes.Repeat([]byte{'x'}, len(progress)))
	out := buf.Process(context.Background(), []byte(progress+partial))
	require.Equal(t, progress+string(oversized), string(out))
	require.True(t, buf.done)
}

func TestGuardrailsResponseBuffer_SSEDeclaredButRawJSONBody_Replaced(t *testing.T) {
	malformed := []byte("malformed response replacement")
	var checked int
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		checked++
		return nil
	}).withMalformedResponse(malformed)

	body := []byte("{\n\n\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}")
	require.Equal(t, string(malformed), string(buf.Process(context.Background(), body)))
	require.True(t, buf.done)
	require.Zero(t, checked, "bare JSON must not reach guardrails")
	require.Nil(t, buf.Flush(context.Background()))
}

func TestGuardrailsResponseBuffer_SSEDeclaredButRawJSONBody_ReplacedAcrossChunks(t *testing.T) {
	malformed := []byte("malformed response replacement")
	var checked int
	buf := newGuardrailsResponseBuffer(true, 1, func(_ context.Context, _ []byte) []byte {
		checked++
		return nil
	}).withMalformedResponse(malformed)

	require.Nil(t, buf.Process(context.Background(), []byte(" \n")))
	require.False(t, buf.done)

	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`)
	require.Equal(t, string(malformed), string(buf.Process(context.Background(), body)))
	require.True(t, buf.done)
	require.Zero(t, checked, "bare JSON must not reach guardrails")
	require.Nil(t, buf.Flush(context.Background()))
	require.Nil(t, buf.Process(context.Background(), []byte("event: message\ndata: {}\n\n")))
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

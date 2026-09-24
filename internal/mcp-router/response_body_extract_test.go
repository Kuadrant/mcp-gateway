package mcprouter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractToolResponseText_PlainJSON(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"hello world"},{"type":"image","data":"abc"}]}}`)
	got, _, ok := extractToolResponseText(body)
	require.True(t, ok)
	require.Equal(t, "hello world", string(got))
}

func TestExtractToolResponseText_PrettyPrintedJSON(t *testing.T) {
	body := []byte(`{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "content": [
      {
        "type": "text",
        "text": "hello world"
      }
    ]
  }
}`)
	got, _, ok := extractToolResponseText(body)
	require.True(t, ok)
	require.Equal(t, "hello world", string(got), "pretty-printed JSON must be decoded as one document, not scanned line by line")
}

func TestExtractToolResponseText_UnicodeEscapedTextKey(t *testing.T) {
	// both the "text" type value and the "text" property name are unicode-
	// escaped (\u0074 == 't'), so a raw substring check for `"text"` in the
	// undecoded bytes would miss this content entirely and skip guardrails.
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"\u0074ext","\u0074ext":"secret"}]}}`)
	got, _, ok := extractToolResponseText(body)
	require.True(t, ok)
	require.Equal(t, "secret", string(got), "unicode-escaped type/text keys must still be decoded and inspected")
}

func TestExtractToolResponseText_MultipleTextItems(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}}`)
	got, _, ok := extractToolResponseText(body)
	require.True(t, ok)
	require.Equal(t, "first\nsecond", string(got))
}

func TestExtractToolResponseText_SSEWrapped(t *testing.T) {
	body := []byte("\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"sse text\"}]}}\n\n")
	got, _, ok := extractToolResponseText(body)
	require.True(t, ok)
	require.Equal(t, "sse text", string(got))
}

func TestExtractToolResponseText_SSECRLF(t *testing.T) {
	// SSE line endings may be CRLF, not just LF.
	body := []byte("event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"sse text\"}]}}\r\n\r\n")
	got, _, ok := extractToolResponseText(body)
	require.True(t, ok)
	require.Equal(t, "sse text", string(got), "CRLF line endings must still be recognized as line boundaries")
}

func TestExtractToolResponseText_SSEExtraSpaceAfterColon(t *testing.T) {
	// only one leading space after "data:" is stripped.
	// The second space is part of the field value.
	// Guardrails must still see the text, not silently skip the
	// check because the data field doesn't start with '{' anymore.
	body := []byte("\nevent: message\ndata:  {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"sse text\"}]}}\n\n")
	got, _, ok := extractToolResponseText(body)
	require.True(t, ok)
	require.Equal(t, "sse text", string(got), "guardrails must inspect text even with an extra space after the SSE data: colon")
}

func TestExtractToolResponseText_NoTextContent(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"image","data":"abc"}]}}`)
	got, _, ok := extractToolResponseText(body)
	require.True(t, ok, "a decodable result with no text items is not a failure")
	require.Nil(t, got, "nil when no text items found")
}

func TestExtractToolResponseText_EmptyContent(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`)
	got, _, ok := extractToolResponseText(body)
	require.True(t, ok)
	require.Nil(t, got)
}

func TestExtractToolResponseText_EmptyBody(t *testing.T) {
	got, _, ok := extractToolResponseText(nil)
	require.True(t, ok)
	require.Nil(t, got)
	got, _, ok = extractToolResponseText([]byte{})
	require.True(t, ok)
	require.Nil(t, got)
}

func TestExtractToolResponseText_NotAResult(t *testing.T) {
	// requests and notifications have neither a result nor an error - this
	// isn't a decodable JSON-RPC response at all, so guardrails can't
	// evaluate it and must fail closed rather than silently pass it through.
	body := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":1,"params":{}}`)
	got, _, ok := extractToolResponseText(body)
	require.False(t, ok)
	require.Nil(t, got)
}

func TestExtractToolResponseText_ErrorResponse(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"bad request"}}`)
	got, _, ok := extractToolResponseText(body)
	require.True(t, ok, "an error response has no tool result content to check, which is not a decode failure")
	require.Nil(t, got)
}

func TestExtractToolResponseText_UndecodableBody(t *testing.T) {
	// not valid JSON at all: must fail closed (ok=false), not be treated the
	// same as a legitimately empty/no-text result.
	body := []byte(`not json`)
	got, _, ok := extractToolResponseText(body)
	require.False(t, ok)
	require.Nil(t, got)
}

func TestExtractToolResponseText_UndecodableResult(t *testing.T) {
	// "result" present but not a tool-call-shaped payload: must fail closed.
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":"not an object"}`)
	got, _, ok := extractToolResponseText(body)
	require.False(t, ok)
	require.Nil(t, got)
}

func TestExtractToolResponseText_MultipleSSEEvents(t *testing.T) {
	// Multiple SSE events are joined with '\n' into a single string for the
	// guardrails check. If the check returns StatusModified, the replacement
	// body is a single SSE event with the merged text — the original multi-event
	// structure is not preserved. In practice tools/call responses are single-event.
	// each event is terminated by its own blank line, per the SSE spec.
	body := []byte(
		"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"part one\"}]}}\n\n" +
			"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"part two\"}]}}\n\n",
	)
	got, _, ok := extractToolResponseText(body)
	require.True(t, ok)
	require.Equal(t, "part one\npart two", string(got))
}

func TestExtractToolResponseText_SSEEventWithMultipleDataLines(t *testing.T) {
	// per the SSE spec, multiple data: lines within one event are joined with
	// '\n' before the field is parsed as a single JSON document. Splitting a
	// tool result's JSON body across several data: lines is valid SSE framing
	// and must still be decoded, not skipped.
	body := []byte(
		"event: message\n" +
			"data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\n" +
			"data: \"text\":\"reassembled\"}]}}\n" +
			"\n",
	)
	got, _, ok := extractToolResponseText(body)
	require.True(t, ok)
	require.Equal(t, "reassembled", string(got), "data: lines split across one event must be reassembled before parsing")
}

func TestExtractToolResponseText_EmptyTextSkipped(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":""},{"type":"text","text":"real"}]}}`)
	got, _, ok := extractToolResponseText(body)
	require.True(t, ok)
	require.Equal(t, "real", string(got))
}

func TestExtractToolResponseText_IsErrorResult(t *testing.T) {
	// isError:true is an application-level error, not a JSON-RPC error.
	// guardrails must still inspect the text — upstream error details may contain PII.
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"upstream error: connection refused to 192.168.1.1:5432"}]}}`)
	got, isError, ok := extractToolResponseText(body)
	require.True(t, ok)
	require.True(t, isError, "the upstream's failed outcome must be reported so a redaction can preserve it")
	require.Equal(t, "upstream error: connection refused to 192.168.1.1:5432", string(got),
		"text in isError results must be extracted for guardrails inspection")
}

func TestExtractToolResponseText_IsErrorSSE(t *testing.T) {
	body := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"isError\":true,\"content\":[{\"type\":\"text\",\"text\":\"failed\"}]}}\n\n")
	_, isError, ok := extractToolResponseText(body)
	require.True(t, ok)
	require.True(t, isError)
}

func TestExtractToolResponseText_SuccessIsNotError(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`)
	_, isError, ok := extractToolResponseText(body)
	require.True(t, ok)
	require.False(t, isError)
}

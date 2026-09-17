package mcprouter

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractToolResponseText_PlainJSON(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"hello world"},{"type":"image","data":"abc"}]}}`)
	got := extractToolResponseText(body)
	require.Equal(t, "hello world", string(got))
}

func TestExtractToolResponseText_MultipleTextItems(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"first"},{"type":"text","text":"second"}]}}`)
	got := extractToolResponseText(body)
	require.Equal(t, "first\nsecond", string(got))
}

func TestExtractToolResponseText_SSEWrapped(t *testing.T) {
	body := []byte("\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"sse text\"}]}}\n\n")
	got := extractToolResponseText(body)
	require.Equal(t, "sse text", string(got))
}

func TestExtractToolResponseText_NoTextContent(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"image","data":"abc"}]}}`)
	got := extractToolResponseText(body)
	require.Nil(t, got, "nil when no text items found")
}

func TestExtractToolResponseText_EmptyContent(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[]}}`)
	got := extractToolResponseText(body)
	require.Nil(t, got)
}

func TestExtractToolResponseText_EmptyBody(t *testing.T) {
	got := extractToolResponseText(nil)
	require.Nil(t, got)
	got = extractToolResponseText([]byte{})
	require.Nil(t, got)
}

func TestExtractToolResponseText_NotAResult(t *testing.T) {
	// requests and notifications have method fields, not results
	body := []byte(`{"jsonrpc":"2.0","method":"tools/call","id":1,"params":{}}`)
	got := extractToolResponseText(body)
	require.Nil(t, got)
}

func TestExtractToolResponseText_ErrorResponse(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"bad request"}}`)
	got := extractToolResponseText(body)
	require.Nil(t, got)
}

func TestExtractToolResponseText_MultipleSSEEvents(t *testing.T) {
	// Multiple SSE events are joined with '\n' into a single string for the
	// guardrails check. If the check returns StatusModified, the replacement
	// body is a single SSE event with the merged text — the original multi-event
	// structure is not preserved. In practice tools/call responses are single-event.
	body := []byte(
		"\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"part one\"}]}}\n" +
			"event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"part two\"}]}}\n\n",
	)
	got := extractToolResponseText(body)
	require.Equal(t, "part one\npart two", string(got))
}

func TestExtractToolResponseText_EmptyTextSkipped(t *testing.T) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":""},{"type":"text","text":"real"}]}}`)
	got := extractToolResponseText(body)
	require.Equal(t, "real", string(got))
}

func TestExtractToolResponseText_IsErrorResult(t *testing.T) {
	// isError:true is an application-level error, not a JSON-RPC error.
	// guardrails must still inspect the text — upstream error details may contain PII.
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"upstream error: connection refused to 192.168.1.1:5432"}]}}`)
	got := extractToolResponseText(body)
	require.Equal(t, "upstream error: connection refused to 192.168.1.1:5432", string(got),
		"text in isError results must be extracted for guardrails inspection")
}

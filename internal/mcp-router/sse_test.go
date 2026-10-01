package mcprouter

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Kuadrant/mcp-gateway/internal/idmap"
	"github.com/Kuadrant/mcp-gateway/internal/routing"
)

func TestSSELineReader_LineEndings(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n", "\r"} {
		var r sseLineReader
		r.Write([]byte("a" + eol + eol + "b" + eol + "partial"))

		var lines []string
		for {
			raw, line, ok := r.Next()
			if !ok {
				break
			}
			require.Equal(t, string(line)+eol, string(raw))
			lines = append(lines, string(line))
		}
		require.Equal(t, []string{"a", "", "b"}, lines, "eol %q", eol)
		require.Equal(t, "partial", string(r.Pending()))
	}
}

func TestSSELineReader_TrailingCREndsLineImmediately(t *testing.T) {
	var r sseLineReader
	r.Write([]byte("a\r"))
	_, line, ok := r.Next()
	require.True(t, ok, "a CR at the end of a chunk must not wait for the next chunk")
	require.Equal(t, "a", string(line))

	// the LF completing the split CRLF is not an extra blank line
	r.Write([]byte("\nb\n"))
	_, line, ok = r.Next()
	require.True(t, ok)
	require.Equal(t, "b", string(line))
	_, _, ok = r.Next()
	require.False(t, ok)
}

// collectEvents feeds body to an event reader in chunks of size n.
func collectEvents(t *testing.T, body string, n int) []string {
	t.Helper()
	var r sseEventReader
	var events []string
	for i := 0; i < len(body); i += n {
		require.NoError(t, r.Write([]byte(body[i:min(i+n, len(body))])))
		for {
			ev, ok := r.Next()
			if !ok {
				break
			}
			events = append(events, string(ev))
		}
	}
	require.NoError(t, r.Close())
	return events
}

func TestSSEEventReader_NormalizesEveryChunking(t *testing.T) {
	want := []string{
		"event: message\ndata: {\"id\":1}\n\n",
		": keepalive\n\n",
		"data: a\ndata: b\n\n",
	}
	for _, eol := range []string{"\n", "\r\n", "\r"} {
		body := strings.ReplaceAll(strings.Join(want, ""), "\n", eol)
		for n := 1; n <= len(body); n++ {
			require.Equal(t, want, collectEvents(t, body, n), "eol %q chunk %d", eol, n)
		}
	}
}

func TestSSEEventReader_StrayBlankLinesIgnored(t *testing.T) {
	require.Equal(t, []string{"data: x\n\n"}, collectEvents(t, "\n\r\n\rdata: x\n\n\n", 64))
}

func TestSSEEventReader_BareJSON(t *testing.T) {
	var r sseEventReader
	require.NoError(t, r.Write([]byte(" \r\n\t")))
	require.ErrorIs(t, r.Write([]byte(`{"jsonrpc":"2.0"}`)), errSSEBareJSON)
}

func TestSSEEventReader_IncompleteAtClose(t *testing.T) {
	for _, body := range []string{"data: x\n", "data: x"} {
		var r sseEventReader
		require.NoError(t, r.Write([]byte(body)))
		_, ok := r.Next()
		require.False(t, ok)
		require.ErrorIs(t, r.Close(), errSSEIncomplete, "body %q", body)
	}
}

func TestSSEEventData(t *testing.T) {
	event := []byte("event: message\ndata:a\ndata: b\nid: 1\ndata:  c\n\n")
	require.Equal(t, "a\nb\n c", string(sseEventData(event)))
	require.Equal(t, "event: message\ndata:a\ndata: b\nid: 1\ndata:  c\n\n", string(event), "event must not be mutated")
	require.Nil(t, sseEventData([]byte("event: message\n\n")))
}

func TestElicitationRewriter_CROnlyWithoutGuardrails(t *testing.T) {
	m, err := idmap.New()
	require.NoError(t, err)
	w := &elicitationRewriter{
		idMap:  m,
		logger: slog.New(slog.NewTextHandler(os.Stdout, nil)),
		req:    &routing.MCPRequest{ServerName: "s1", BackendSessionID: "sess"},
	}

	out := w.Process(context.Background(), []byte("event: message\rdata: {\"jsonrpc\":\"2.0\",\"id\":99,\"method\":\"elicitation/create\",\"params\":{}}\r\r"))
	require.Len(t, w.gatewayIDs, 1, "CR-framed elicitation must be rewritten without waiting for more data")
	require.NotContains(t, string(out), `"id":99`)
	require.True(t, strings.HasSuffix(string(out), "\r\r"), "original line endings preserved")
}

func TestExtractToolResponseText_SSETruncatedFailsClosed(t *testing.T) {
	_, _, ok := extractToolResponseText([]byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[]}}\n"))
	require.False(t, ok)
}

package mcprouter

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
)

func newTestResourceRewriter(prefix string) *resourceURIRewriter {
	return &resourceURIRewriter{
		prefix: prefix,
		logger: slog.New(slog.DiscardHandler),
	}
}

func processAndFlush(t *testing.T, r *resourceURIRewriter, chunks ...[]byte) []byte {
	t.Helper()
	ctx := context.Background()
	var out []byte
	for _, c := range chunks {
		out = append(out, r.Process(ctx, c)...)
	}
	out = append(out, r.Flush(ctx)...)
	return out
}

func TestResourceURIRewriter_PlainJSON_RewritesUIResourceURI(t *testing.T) {
	r := newTestResourceRewriter("insights_")
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"hi"}],"_meta":{"ui":{"resourceUri":"ui://template.html"}}}}`)

	got := processAndFlush(t, r, body)

	want := `{"jsonrpc":"2.0","id":1,"result":{"_meta":{"ui":{"resourceUri":"ui://insights_template.html"}},"content":[{"type":"text","text":"hi"}]}}`
	if string(got) != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestResourceURIRewriter_SSEFramed_RewritesUIResourceURI(t *testing.T) {
	r := newTestResourceRewriter("insights_")
	body := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"_meta\":{\"ui\":{\"resourceUri\":\"ui://template.html\"}}}}\n\n")

	got := processAndFlush(t, r, body)

	if !contains(got, `"resourceUri":"ui://insights_template.html"`) {
		t.Errorf("expected rewritten resourceUri in output, got: %s", got)
	}
	if !contains(got, "event: message\n") {
		t.Errorf("expected non-data SSE lines to pass through unchanged, got: %s", got)
	}
}

func TestResourceURIRewriter_ChunkedAcrossMultipleProcessCalls(t *testing.T) {
	r := newTestResourceRewriter("insights_")
	full := `{"jsonrpc":"2.0","id":1,"result":{"_meta":{"ui":{"resourceUri":"ui://template.html"}}}}`
	mid := len(full) / 2

	got := processAndFlush(t, r, []byte(full[:mid]), []byte(full[mid:]))

	if !contains(got, `"resourceUri":"ui://insights_template.html"`) {
		t.Errorf("expected rewritten resourceUri across chunked input, got: %s", got)
	}
}

func TestResourceURIRewriter_NonUISchemeLeftUntouched(t *testing.T) {
	r := newTestResourceRewriter("insights_")
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"_meta":{"ui":{"resourceUri":"https://example.com/template.html"}}}}`)

	got := processAndFlush(t, r, body)

	if !contains(got, `"resourceUri":"https://example.com/template.html"`) {
		t.Errorf("expected non-ui:// URI left untouched, got: %s", got)
	}
}

func TestResourceURIRewriter_NoMetaLeftByteForByteUntouched(t *testing.T) {
	r := newTestResourceRewriter("insights_")
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"hi"}]}}`)

	got := processAndFlush(t, r, body)

	if string(got) != string(body) {
		t.Errorf("expected untouched passthrough\ngot  %s\nwant %s", got, body)
	}
}

func TestResourceURIRewriter_ErrorResultLeftUntouched(t *testing.T) {
	r := newTestResourceRewriter("insights_")
	body := []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"boom"}}`)

	got := processAndFlush(t, r, body)

	if string(got) != string(body) {
		t.Errorf("expected error response left untouched\ngot  %s\nwant %s", got, body)
	}
}

func TestResourceURIRewriter_MalformedJSONDoesNotPanic(t *testing.T) {
	r := newTestResourceRewriter("insights_")
	body := []byte(`{not valid json`)

	got := processAndFlush(t, r, body)

	if string(got) != string(body) {
		t.Errorf("expected malformed input left untouched\ngot  %s\nwant %s", got, body)
	}
}

func TestResourceURIRewriter_EmptyBody(t *testing.T) {
	r := newTestResourceRewriter("insights_")

	got := processAndFlush(t, r)

	if len(got) != 0 {
		t.Errorf("expected empty output for empty body, got: %s", got)
	}
}

func TestResourceURIRewriter_EmptyPrefixLeavesURIUnprefixed(t *testing.T) {
	r := newTestResourceRewriter("")
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"_meta":{"ui":{"resourceUri":"ui://template.html"}}}}`)

	got := processAndFlush(t, r, body)

	if !contains(got, `"resourceUri":"ui://template.html"`) {
		t.Errorf("expected empty prefix to leave URI unchanged, got: %s", got)
	}
}

func TestResourceURIRewriter_FlushTwiceIsSafe(t *testing.T) {
	r := newTestResourceRewriter("insights_")
	ctx := context.Background()
	body := []byte(`{"jsonrpc":"2.0","id":1,"result":{"_meta":{"ui":{"resourceUri":"ui://template.html"}}}}`)

	r.Process(ctx, body)
	first := r.Flush(ctx)
	second := r.Flush(ctx)

	if len(second) != 0 {
		t.Errorf("expected second Flush to be a no-op, got: %s", second)
	}
	if !contains(first, `"resourceUri":"ui://insights_template.html"`) {
		t.Errorf("expected first Flush to contain rewritten URI, got: %s", first)
	}
}

// guards against holding a complete line until Flush, which would deadlock a
// live elicitation exchange composed on the same response.
func TestResourceURIRewriter_CompleteLineForwardedByProcessNotHeldUntilFlush(t *testing.T) {
	r := newTestResourceRewriter("insights_")
	ctx := context.Background()
	line := []byte("data: {\"jsonrpc\":\"2.0\",\"method\":\"elicitation/create\",\"id\":1,\"params\":{}}\n")

	out := r.Process(ctx, line)

	if len(out) == 0 {
		t.Fatal("expected complete line to be forwarded immediately by Process, got nothing (would deadlock a live elicitation exchange)")
	}
	if !contains(out, "elicitation/create") {
		t.Errorf("expected forwarded output to contain the original message, got: %s", out)
	}
}

func TestResourceURIRewriter_PartialLineHeldUntilComplete(t *testing.T) {
	r := newTestResourceRewriter("insights_")
	ctx := context.Background()

	out := r.Process(ctx, []byte(`{"jsonrpc":"2.0","id":1,"result":`))
	if len(out) != 0 {
		t.Errorf("expected no output for an incomplete line, got: %s", out)
	}

	out = r.Process(ctx, []byte("{\"_meta\":{\"ui\":{\"resourceUri\":\"ui://template.html\"}}}}\n"))
	if !contains(out, `"resourceUri":"ui://insights_template.html"`) {
		t.Errorf("expected rewritten resourceUri once the line completed, got: %s", out)
	}
}

func TestResourceURIRewriter_OversizedLineForwardedUnrewrittenNotBufferedForever(t *testing.T) {
	r := newTestResourceRewriter("insights_")
	ctx := context.Background()

	oversized := bytes.Repeat([]byte("x"), maxBufferedLineBytes+1)
	out := r.Process(ctx, oversized)
	if len(out) != len(oversized) {
		t.Fatalf("expected the oversized unterminated chunk to be forwarded once the cap is exceeded, got %d bytes, want %d", len(out), len(oversized))
	}
	if r.buf != nil {
		t.Errorf("expected buf to be cleared after overflow, got %d bytes still buffered", len(r.buf))
	}

	// the rest of the abandoned line, plus its terminator, should still pass through unrewritten
	out = r.Process(ctx, []byte("tail\n"))
	if string(out) != "tail\n" {
		t.Errorf("expected the remainder of the abandoned line to pass through unrewritten, got: %s", out)
	}

	// normal rewriting resumes for the next line
	out = r.Process(ctx, []byte(`{"jsonrpc":"2.0","id":1,"result":{"_meta":{"ui":{"resourceUri":"ui://template.html"}}}}`+"\n"))
	if !contains(out, `"resourceUri":"ui://insights_template.html"`) {
		t.Errorf("expected rewriting to resume on the next line after overflow, got: %s", out)
	}
}

func TestInjectResourcePrefix(t *testing.T) {
	tests := []struct {
		name    string
		uri     string
		prefix  string
		wantURI string
		wantOK  bool
	}{
		{"basic prefix injection", "ui://template.html", "insights", "ui://insights_template.html", true},
		{"prefix already has separator", "ui://template.html", "insights_", "ui://insights_template.html", true},
		{"non-ui scheme untouched", "https://example.com/x.html", "insights", "https://example.com/x.html", false},
		{"malformed uri untouched", "ui://\x7fbad", "insights", "ui://\x7fbad", false},
		{"empty prefix untouched host", "ui://template.html", "", "ui://template.html", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := injectResourcePrefix(tt.uri, tt.prefix)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.wantURI {
				t.Errorf("got %q, want %q", got, tt.wantURI)
			}
		})
	}
}

func contains(haystack []byte, needle string) bool {
	return bytes.Contains(haystack, []byte(needle))
}

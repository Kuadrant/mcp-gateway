package otel

import (
	"context"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	internaljwt "github.com/Kuadrant/mcp-gateway/internal/jwt"
)

// the mcp go-sdk embeds backend session IDs in error text in three formats:
//
//	"(session ID: %v)"            streamable client transport errors
//	"session IDs %q and %q"       mismatching session IDs
//	"session ID %q"               MemoryEventStore
//
// session IDs may contain any visible ASCII (0x21-0x7E), including ')' and
// '"', so matching follows the sdk's formatting rather than a character class.
const sdkSessionMarker = "session ID"

const goQuoted = `"(?:[^"\\]|\\.)*"`

var (
	// %v form, tried in order:
	//  1. up to the sdk terminator, ")" then ": ", a newline or end of text;
	//     also covers non-conforming IDs with spaces (the go-sdk does not
	//     enforce the charset)
	//  2. wrapped with trailing text: the longest non-space run before a ")",
	//     exact for spec IDs
	//  3. spaces and trailing text: up to the last ")" on the line; may
	//     over-redact, never leaks part of the ID
	sdkSessionIDPattern = regexp.MustCompile(`session ID: (?:([^\n]+?)\)(: |\n|$)|(\S+)\)|([^\n]+)\))`)
	// %q forms: a full Go-quoted string, escapes included
	sdkSessionIDsPattern      = regexp.MustCompile(`session IDs (` + goQuoted + `) and (` + goQuoted + `)`)
	sdkQuotedSessionIDPattern = regexp.MustCompile(`session ID (` + goQuoted + `)`)
)

func scrubSessionIDs(s string) string {
	if !strings.Contains(s, sdkSessionMarker) {
		return s
	}
	s = sdkSessionIDsPattern.ReplaceAllStringFunc(s, func(m string) string {
		ids := sdkSessionIDsPattern.FindStringSubmatch(m)
		return "session IDs " + safeQuoted(ids[1]) + " and " + safeQuoted(ids[2])
	})
	s = sdkQuotedSessionIDPattern.ReplaceAllStringFunc(s, func(m string) string {
		return "session ID " + safeQuoted(sdkQuotedSessionIDPattern.FindStringSubmatch(m)[1])
	})
	return sdkSessionIDPattern.ReplaceAllStringFunc(s, func(m string) string {
		g := sdkSessionIDPattern.FindStringSubmatch(m)
		switch {
		case g[1] != "":
			return "session ID: " + internaljwt.LogSafeSessionID(g[1]) + ")" + g[2]
		case g[3] != "":
			return "session ID: " + internaljwt.LogSafeSessionID(g[3]) + ")"
		default:
			return "session ID: " + internaljwt.LogSafeSessionID(g[4]) + ")"
		}
	})
}

// safeQuoted unquotes a %q session ID so the hash matches LogSafeSessionID of
// the raw value elsewhere in the logs.
func safeQuoted(q string) string {
	id, err := strconv.Unquote(q)
	if err != nil {
		return `"[redacted]"`
	}
	return strconv.Quote(internaljwt.LogSafeSessionID(id))
}

// sessionScrubHandler rewrites sdk-embedded session IDs to LogSafeSessionID form from the message and
// from error and string attributes before records reach any sink.
type sessionScrubHandler struct {
	next slog.Handler
}

func newSessionScrubHandler(next slog.Handler) slog.Handler {
	return &sessionScrubHandler{next: next}
}

func (h *sessionScrubHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h *sessionScrubHandler) Handle(ctx context.Context, r slog.Record) error {
	dirty := strings.Contains(r.Message, sdkSessionMarker)
	if !dirty {
		r.Attrs(func(a slog.Attr) bool {
			dirty = attrHasSession(a)
			return !dirty
		})
	}
	if !dirty {
		return h.next.Handle(ctx, r)
	}
	out := slog.NewRecord(r.Time, r.Level, scrubSessionIDs(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(scrubAttr(a))
		return true
	})
	return h.next.Handle(ctx, out)
}

func (h *sessionScrubHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	scrubbed := make([]slog.Attr, len(attrs))
	for i := range attrs {
		scrubbed[i] = scrubAttr(attrs[i])
	}
	return &sessionScrubHandler{next: h.next.WithAttrs(scrubbed)}
}

func (h *sessionScrubHandler) WithGroup(name string) slog.Handler {
	return &sessionScrubHandler{next: h.next.WithGroup(name)}
}

// attrText returns the text a string or error attribute renders as. LogValuers
// are not resolved here so lazy debug values are not computed twice.
func attrText(v slog.Value) (string, bool) {
	if v.Kind() == slog.KindString {
		return v.String(), true
	}
	if v.Kind() == slog.KindAny {
		if err, ok := v.Any().(error); ok && err != nil {
			return err.Error(), true
		}
	}
	return "", false
}

func attrHasSession(a slog.Attr) bool {
	v := a.Value
	if v.Kind() == slog.KindGroup {
		for _, g := range v.Group() {
			if attrHasSession(g) {
				return true
			}
		}
		return false
	}
	s, ok := attrText(v)
	return ok && strings.Contains(s, sdkSessionMarker)
}

func scrubAttr(a slog.Attr) slog.Attr {
	v := a.Value
	if v.Kind() == slog.KindGroup {
		group := v.Group()
		scrubbed := make([]slog.Attr, len(group))
		for i := range group {
			scrubbed[i] = scrubAttr(group[i])
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(scrubbed...)}
	}
	s, ok := attrText(v)
	if !ok || !strings.Contains(s, sdkSessionMarker) {
		return a
	}
	return slog.String(a.Key, scrubSessionIDs(s))
}

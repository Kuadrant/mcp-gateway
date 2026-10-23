package mcprouter

import (
	"bytes"
	"errors"
)

// all router SSE parsing goes through this file so framing rules cannot drift
// between the guardrails, elicitation and resource rewrite paths. see
// https://html.spec.whatwg.org/multipage/server-sent-events.html#parsing-an-event-stream

var dataPrefix = []byte("data:")

var (
	// errSSEBareJSON means an SSE-declared body is a bare JSON document.
	errSSEBareJSON = errors.New("sse: body is bare JSON, not an event stream")
	// errSSEIncomplete means the stream ended inside an event.
	errSSEIncomplete = errors.New("sse: stream ended without event delimiter")
)

// sseLineReader splits an incrementally received body into lines. LF, CRLF
// and lone CR all terminate a line. A CR at the end of the buffer ends its
// line immediately, so a CR-only event is never held waiting for the next
// chunk; a leading LF in that next chunk is then treated as the rest of the
// CRLF pair and dropped.
type sseLineReader struct {
	buf    []byte
	skipLF bool
}

// Write appends chunk to the unread buffer.
func (r *sseLineReader) Write(chunk []byte) {
	if r.skipLF && len(chunk) > 0 {
		r.skipLF = false
		if chunk[0] == '\n' {
			chunk = chunk[1:]
		}
	}
	r.buf = append(r.buf, chunk...)
}

// Next returns the next complete line. raw includes the terminator, line
// does not.
func (r *sseLineReader) Next() (raw, line []byte, ok bool) {
	i := bytes.IndexAny(r.buf, "\r\n")
	if i == -1 {
		return nil, nil, false
	}
	end := i + 1
	if r.buf[i] == '\r' {
		if end == len(r.buf) {
			r.skipLF = true
		} else if r.buf[end] == '\n' {
			end++
		}
	}
	raw, line = r.buf[:end], r.buf[:i]
	r.buf = r.buf[end:]
	return raw, line, true
}

// Pending returns the unterminated trailing bytes.
func (r *sseLineReader) Pending() []byte {
	return r.buf
}

// Reset discards all buffered state.
func (r *sseLineReader) Reset() {
	r.buf = nil
	r.skipLF = false
}

// sseEventReader assembles complete events from an incrementally received
// event stream. Events are re-encoded with LF line endings so downstream
// consumers only ever see one framing form.
type sseEventReader struct {
	lines   sseLineReader
	event   []byte // LF-terminated lines of the open event
	started bool   // first non-whitespace byte seen
}

// Write appends chunk. It returns errSSEBareJSON if the first non-whitespace
// byte of the stream opens a JSON object.
func (r *sseEventReader) Write(chunk []byte) error {
	if !r.started {
		if t := bytes.TrimLeft(chunk, " \t\r\n"); len(t) > 0 {
			r.started = true
			if t[0] == '{' {
				return errSSEBareJSON
			}
		}
	}
	r.lines.Write(chunk)
	return nil
}

// Next returns the next complete event, terminated by a blank line.
func (r *sseEventReader) Next() ([]byte, bool) {
	for {
		_, line, ok := r.lines.Next()
		if !ok {
			return nil, false
		}
		if len(line) != 0 {
			r.event = append(append(r.event, line...), '\n')
			continue
		}
		if len(r.event) == 0 {
			continue // blank line with no open event dispatches nothing
		}
		event := append(r.event, '\n')
		r.event = nil
		return event, true
	}
}

// Buffered returns the size of the open event plus any partial line.
func (r *sseEventReader) Buffered() int {
	return len(r.event) + len(r.lines.Pending())
}

// Close reports errSSEIncomplete if the stream ended inside an event.
func (r *sseEventReader) Close() error {
	if r.Buffered() > 0 {
		return errSSEIncomplete
	}
	return nil
}

// sseDataValue reports whether line is a data field and returns its value
// with the optional single leading space removed.
func sseDataValue(line []byte) ([]byte, bool) {
	v, ok := bytes.CutPrefix(line, dataPrefix)
	if !ok {
		return nil, false
	}
	return bytes.TrimPrefix(v, []byte{' '}), true
}

// sseEventData returns the data field of one LF-framed event (as produced by
// sseEventReader): every data line's value joined with LF.
func sseEventData(event []byte) []byte {
	var data []byte
	found, owned := false, false
	for len(event) > 0 {
		var line []byte
		line, event, _ = bytes.Cut(event, []byte{'\n'})
		v, ok := sseDataValue(line)
		if !ok {
			continue
		}
		switch {
		case !found:
			data, found = v, true // single data line: no copy
		case !owned:
			data = append(append(append(make([]byte, 0, len(data)+1+len(v)), data...), '\n'), v...)
			owned = true
		default:
			data = append(append(data, '\n'), v...)
		}
	}
	return data
}

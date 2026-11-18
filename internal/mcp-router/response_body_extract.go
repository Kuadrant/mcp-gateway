package mcprouter

import (
	"bytes"
	"encoding/json"
	"strings"
)

// extractToolResponseText pulls out and joins all the text from a tool
// call's result, whether it's plain JSON or an SSE stream. isError reports
// whether the upstream marked the result as a failed call, so a redacted
// replacement can keep that outcome. inputRequired reports a 2026-07-28
// input_required result; the caller decides what that means for its
// protocol version. ok is false when
// the body could not be reliably parsed as a JSON-RPC response - callers
// must treat that as "guardrails could not evaluate this", never as
// "nothing to check", since silently forwarding undecodable content would
// let a malformed or adversarial upstream bypass guardrails. When ok is
// true, a nil text means the result was decoded but genuinely carries no
// text content (e.g. image-only), which is safe to pass through unchanged.
func extractToolResponseText(body []byte) (text []byte, isError, inputRequired, ok bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		texts, isError, inputRequired, ok := extractTextFromResultJSON(trimmed)
		return joinTexts(texts), isError, inputRequired, ok
	}
	texts, isError, inputRequired, ok := extractSSETexts(body)
	return joinTexts(texts), isError, inputRequired, ok
}

// extractSSETexts splits body into SSE events (separated by blank lines)
// and extracts the text from each one. Events with a data: field spread
// across multiple lines are stitched back together first so they parse.
// CRLF and lone CR line endings are normalized to LF first, per the SSE
// spec. isError is true if any event is an isError result. ok is false if
// any event failed to decode - see extractTextFromResultJSON.
func extractSSETexts(body []byte) (texts []string, isError, inputRequired, ok bool) {
	body = normalizeLineEndings(body)
	ok = true
	var eventBytes []byte
	remaining := body
	for len(remaining) > 0 {
		idx := bytes.IndexByte(remaining, '\n')
		var line []byte
		if idx == -1 {
			line = remaining
			remaining = nil
		} else {
			line = remaining[:idx+1]
			remaining = remaining[idx+1:]
		}
		if len(bytes.TrimSpace(line)) != 0 {
			eventBytes = append(eventBytes, line...)
			continue
		}
		if len(eventBytes) == 0 {
			continue // stray/leading blank line, not a real event boundary
		}
		// blank line: the event assembled so far is complete
		event := append(eventBytes, line...)
		eventBytes = nil
		eventTexts, eventIsError, eventInputRequired, eventOK := extractSSEEventTexts(event)
		texts = append(texts, eventTexts...)
		isError = isError || eventIsError
		inputRequired = inputRequired || eventInputRequired
		ok = ok && eventOK
	}
	// a trailing event with no terminating blank line (e.g. a truncated body)
	if len(eventBytes) > 0 {
		eventTexts, eventIsError, eventInputRequired, eventOK := extractSSEEventTexts(eventBytes)
		texts = append(texts, eventTexts...)
		isError = isError || eventIsError
		inputRequired = inputRequired || eventInputRequired
		ok = ok && eventOK
	}
	return texts, isError, inputRequired, ok
}

// normalizeLineEndings rewrites CRLF and lone CR line endings to LF so the
// '\n'-based splitting above and in sseEventData handles any of the three
// line-ending forms the SSE spec allows.
func normalizeLineEndings(b []byte) []byte {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
}

// extractSSEEventTexts decodes one complete SSE event's reassembled data:
// field as a tools/call result JSON document.
func extractSSEEventTexts(event []byte) (texts []string, isError, inputRequired, ok bool) {
	data := sseEventData(event)
	if len(data) == 0 {
		return nil, false, false, false
	}
	return extractTextFromResultJSON(data)
}

func joinTexts(texts []string) []byte {
	if len(texts) == 0 {
		return nil
	}
	return []byte(strings.Join(texts, "\n"))
}

type toolResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolCallResultPayload struct {
	Content []toolResultContent `json:"content"`
	IsError bool                `json:"isError"`
	// raw so an odd value from a 2025 upstream can't fail the decode
	ResultType json.RawMessage `json:"resultType"`
}

// isInputRequired decodes the raw value rather than comparing bytes, so an
// escaped form like "\u0069nput_required" still counts. non-strings are false.
func isInputRequired(raw json.RawMessage) bool {
	var rt string
	return len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &rt) == nil && rt == "input_required"
}

// extractTextFromResultJSON extracts text content from a JSON-RPC response's
// result. ok is false when data can't be reliably parsed as a JSON-RPC
// response with a decodable result - callers must fail closed on that, not
// treat it the same as "no text", or a malformed/ambiguous body would bypass
// guardrails unchecked. An error response has no tool result content to
// check either way, so it's ok=true with nil text, same as a result with no
// text items.
func extractTextFromResultJSON(data []byte) (texts []string, isError, inputRequired, ok bool) {
	// reuse jsonRPCMessage from elicitation.go
	var msg jsonRPCMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, false, false, false
	}
	if len(msg.Result) == 0 {
		// an error response has no tool result content to check either way;
		// neither result nor error present means this isn't a decodable
		// JSON-RPC response at all.
		return nil, false, false, len(msg.Error) != 0
	}
	var payload toolCallResultPayload
	if err := json.Unmarshal(msg.Result, &payload); err != nil {
		return nil, false, false, false
	}
	for i := range payload.Content {
		if payload.Content[i].Type == "text" && payload.Content[i].Text != "" {
			texts = append(texts, payload.Content[i].Text)
		}
	}
	return texts, payload.IsError, isInputRequired(payload.ResultType), true
}

package mcprouter

import (
	"bytes"
	"encoding/json"
	"strings"
)

// extractToolResponseText pulls out and joins all the text from a tool
// call's result, whether it's plain JSON or an SSE stream. isError reports
// whether the upstream marked the result as a failed call, so a redacted
// replacement can keep that outcome. ok is false when
// the body could not be reliably parsed as a JSON-RPC response - callers
// must treat that as "guardrails could not evaluate this", never as
// "nothing to check", since silently forwarding undecodable content would
// let a malformed or adversarial upstream bypass guardrails. When ok is
// true, a nil text means the result was decoded but genuinely carries no
// text content (e.g. image-only), which is safe to pass through unchanged.
func extractToolResponseText(body []byte) (text []byte, isError, ok bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		texts, isError, ok := extractTextFromResultJSON(trimmed)
		return joinTexts(texts), isError, ok
	}
	texts, isError, ok := extractSSETexts(body)
	return joinTexts(texts), isError, ok
}

// extractSSETexts splits body into SSE events and extracts the text from
// each one. isError is true if any event is an isError result. ok is false
// if any event failed to decode (see extractTextFromResultJSON) or the body
// ends mid-event.
func extractSSETexts(body []byte) (texts []string, isError, ok bool) {
	var r sseEventReader
	r.lines.Write(body) // bare JSON is routed to extractTextFromResultJSON by the caller
	ok = true
	for {
		event, more := r.Next()
		if !more {
			break
		}
		eventTexts, eventIsError, eventOK := extractSSEEventTexts(event)
		texts = append(texts, eventTexts...)
		isError = isError || eventIsError
		ok = ok && eventOK
	}
	return texts, isError, ok && r.Close() == nil
}

// extractSSEEventTexts decodes one complete SSE event's reassembled data:
// field as a tools/call result JSON document.
func extractSSEEventTexts(event []byte) (texts []string, isError, ok bool) {
	data := sseEventData(event)
	if len(data) == 0 {
		return nil, false, false
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
}

// extractTextFromResultJSON extracts text content from a JSON-RPC response's
// result. ok is false when data can't be reliably parsed as a JSON-RPC
// response with a decodable result - callers must fail closed on that, not
// treat it the same as "no text", or a malformed/ambiguous body would bypass
// guardrails unchecked. An error response has no tool result content to
// check either way, so it's ok=true with nil text, same as a result with no
// text items.
func extractTextFromResultJSON(data []byte) (texts []string, isError, ok bool) {
	// reuse jsonRPCMessage from elicitation.go
	var msg jsonRPCMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, false, false
	}
	if len(msg.Result) == 0 {
		// an error response has no tool result content to check either way;
		// neither result nor error present means this isn't a decodable
		// JSON-RPC response at all.
		return nil, false, len(msg.Error) != 0
	}
	var payload toolCallResultPayload
	if err := json.Unmarshal(msg.Result, &payload); err != nil {
		return nil, false, false
	}
	for i := range payload.Content {
		if payload.Content[i].Type == "text" && payload.Content[i].Text != "" {
			texts = append(texts, payload.Content[i].Text)
		}
	}
	return texts, payload.IsError, true
}

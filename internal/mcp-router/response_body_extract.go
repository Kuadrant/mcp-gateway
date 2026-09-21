package mcprouter

import (
	"bytes"
	"encoding/json"
	"strings"
)

// extractToolResponseText pulls out and joins all the text from a tool
// call's result, whether it's plain JSON or an SSE stream. Returns nil if
// there's no text, meaning guardrails have nothing to check.
func extractToolResponseText(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		// plain JSON - decode the whole body as one document.
		return joinTexts(extractTextFromResultJSON(trimmed))
	}
	return joinTexts(extractSSETexts(body))
}

// extractSSETexts splits body into SSE events (separated by blank lines)
// and extracts the text from each one. Events with a data: field spread
// across multiple lines are stitched back together first so they parse.
func extractSSETexts(body []byte) []string {
	var texts []string
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
		// blank line: the event assembled so far is complete
		event := append(eventBytes, line...)
		eventBytes = nil
		texts = append(texts, extractSSEEventTexts(event)...)
	}
	// a trailing event with no terminating blank line (e.g. a truncated body)
	if len(eventBytes) > 0 {
		texts = append(texts, extractSSEEventTexts(eventBytes)...)
	}
	return texts
}

// extractSSEEventTexts decodes one complete SSE event's reassembled data:
// field as a tools/call result JSON document.
func extractSSEEventTexts(event []byte) []string {
	data := sseEventData(event)
	if len(data) == 0 || data[0] != '{' {
		return nil
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
}

func extractTextFromResultJSON(data []byte) []string {
	// reuse jsonRPCMessage from elicitation.go
	var msg jsonRPCMessage
	if err := json.Unmarshal(data, &msg); err != nil || msg.Method != "" || len(msg.Result) == 0 {
		// skip requests (have Method), notifications, and error responses (no Result)
		return nil
	}
	var payload toolCallResultPayload
	if err := json.Unmarshal(msg.Result, &payload); err != nil {
		return nil
	}
	var texts []string
	for i := range payload.Content {
		if payload.Content[i].Type == "text" && payload.Content[i].Text != "" {
			texts = append(texts, payload.Content[i].Text)
		}
	}
	return texts
}

package mcprouter

import (
	"bytes"
	"encoding/json"
	"strings"
)

// extractToolResponseText extracts and concatenates the text from all
// type:"text" content items in a tools/call result body. The body may be
// plain JSON or SSE. Returns nil when no text content is found.
func extractToolResponseText(body []byte) []byte {
	var texts []string
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
		trimmed := bytes.TrimSpace(line)
		jsonData := trimmed
		if bytes.HasPrefix(trimmed, dataPrefix) {
			jsonData = bytes.TrimSpace(bytes.TrimPrefix(trimmed, dataPrefix))
		}
		if len(jsonData) == 0 || jsonData[0] != '{' {
			continue
		}
		texts = append(texts, extractTextFromResultJSON(jsonData)...)
	}
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
	// fast path: skip the second unmarshal when there are no text-type items
	if !bytes.Contains(msg.Result, []byte(`"text"`)) {
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

// Package nemo implements the guardrails Transformer for NeMo Guardrails,
// translating between MCP and NeMo's /v1/guardrail/checks schema.
package nemo

import (
	"encoding/json"
	"fmt"
)

// Status values in CheckResponse.Status.
const (
	StatusSuccess  = "success"
	StatusModified = "modified"
	StatusBlocked  = "blocked"
)

// CheckRequest is the request body for NeMo's /v1/guardrail/checks endpoint.
type CheckRequest struct {
	Model      string           `json:"model"`
	Messages   []Message        `json:"messages"`
	Guardrails GuardrailsConfig `json:"guardrails"`
}

// Message is a single message in a CheckRequest.
type Message struct {
	Role    string `json:"role"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content"`
	Config  string `json:"config,omitempty"`
}

// GuardrailsConfig stores the merged global+per-server config IDs.
type GuardrailsConfig struct {
	ConfigIDs []string `json:"config_ids"`
}

// CheckResponse is the response body from NeMo's /v1/guardrail/checks endpoint.
type CheckResponse struct {
	Status  string `json:"status"`
	Content string `json:"content"`
	Rail    string `json:"rail,omitempty"`
}

// Transformer translates between MCP guardrails checks and NeMo's
// /v1/guardrail/checks request/response schema.
type Transformer struct {
	Model string
}

// NewTransformer returns a transformer bound to the model identifier
// resolved from the guardrails Secret.
func NewTransformer(model string) *Transformer {
	return &Transformer{Model: model}
}

// TransformRequest translates a tools/call request into a NeMo
// /v1/guardrail/checks request body. Maps params.name to messages[0].name,
// params.arguments (JSON-encoded) to messages[0].content, and role to
// "user".
func (t *Transformer) TransformRequest(toolName string, arguments json.RawMessage, configIDs []string) ([]byte, error) {
	if toolName == "" {
		return nil, fmt.Errorf("nemo: tool name is required")
	}

	content := "{}"
	if len(arguments) > 0 {
		content = string(arguments)
	}

	req := CheckRequest{
		Model: t.Model,
		Messages: []Message{
			{
				Role:    "user",
				Name:    toolName,
				Content: content,
				Config:  "tool",
			},
		},
		Guardrails: GuardrailsConfig{
			ConfigIDs: nonNilConfigIDs(configIDs),
		},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("nemo: failed to marshal check request: %w", err)
	}
	return body, nil
}

// TransformResponse translates a tools/call result's text content into a NeMo
// /v1/guardrail/checks request body. Maps the tool name (from request context)
// to messages[0].name, the text content to messages[0].content, and role to
// "assistant".
func (t *Transformer) TransformResponse(toolName string, content []byte, configIDs []string) ([]byte, error) {
	if toolName == "" {
		return nil, fmt.Errorf("nemo: tool name is required")
	}

	req := CheckRequest{
		Model: t.Model,
		Messages: []Message{
			{
				Role:    "assistant",
				Name:    toolName,
				Content: string(content),
			},
		},
		Guardrails: GuardrailsConfig{
			ConfigIDs: nonNilConfigIDs(configIDs),
		},
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("nemo: failed to marshal check request: %w", err)
	}
	return body, nil
}

// ParseCheckResponse unmarshals a raw /v1/guardrail/checks HTTP response body.
// An unrecognized Status is a translation failure.
func (t *Transformer) ParseCheckResponse(body []byte) (*CheckResponse, error) {
	var resp CheckResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("nemo: failed to unmarshal check response: %w", err)
	}

	switch resp.Status {
	case StatusSuccess, StatusModified, StatusBlocked:
	default:
		return nil, fmt.Errorf("nemo: unrecognized status %q", resp.Status)
	}

	return &resp, nil
}

// nonNilConfigIDs avoids marshaling a nil slice as JSON null.
func nonNilConfigIDs(configIDs []string) []string {
	if configIDs == nil {
		return []string{}
	}
	return configIDs
}

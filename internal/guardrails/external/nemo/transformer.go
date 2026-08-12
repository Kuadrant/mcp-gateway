// Package nemo implements the guardrails Transformer for NeMo Guardrails,
// translating between MCP and NeMo's /v1/guardrail/checks schema.
package nemo

import (
	"encoding/json"
	"fmt"
)

// Status values in NeMoCheckResponse.Status.
const (
	StatusSuccess  = "success"
	StatusModified = "modified"
	StatusBlocked  = "blocked"
)

// NeMoCheckRequest is the request body for NeMo's /v1/guardrail/checks endpoint.
type NeMoCheckRequest struct {
	Model      string               `json:"model"`
	Messages   []NeMoMessage        `json:"messages"`
	Guardrails NeMoGuardrailsConfig `json:"guardrails"`
}

// NeMoMessage is a single message in a NeMoCheckRequest.
type NeMoMessage struct {
	Role    string `json:"role"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content"`
	Config  string `json:"config,omitempty"`
}

// NeMoGuardrailsConfig stores the merged global+per-server config IDs.
type NeMoGuardrailsConfig struct {
	ConfigIDs []string `json:"config_ids"`
}

// NeMoCheckResponse is the response body from NeMo's /v1/guardrail/checks endpoint.
type NeMoCheckResponse struct {
	Status  string `json:"status"`
	Content string `json:"content"`
	Rail    string `json:"rail,omitempty"`
}

// NeMoTransformer translates between MCP guardrails checks and NeMo's
// /v1/guardrail/checks request/response schema.
type NeMoTransformer struct {
	Model string
}

// NewNeMoTransformer returns a transformer bound to the model identifier
// resolved from the guardrails Secret.
func NewNeMoTransformer(model string) *NeMoTransformer {
	return &NeMoTransformer{Model: model}
}

// TransformRequest translates a tools/call request into a NeMo
// /v1/guardrail/checks request body. Maps params.name to messages[0].name,
// params.arguments (JSON-encoded) to messages[0].content, and role to
// "user".
func (t *NeMoTransformer) TransformRequest(toolName string, arguments json.RawMessage, configIDs []string) ([]byte, error) {
	if toolName == "" {
		return nil, fmt.Errorf("nemo: tool name is required")
	}

	content := "{}"
	if len(arguments) > 0 {
		content = string(arguments)
	}

	req := NeMoCheckRequest{
		Model: t.Model,
		Messages: []NeMoMessage{
			{
				Role:    "user",
				Name:    toolName,
				Content: content,
				Config:  "tool",
			},
		},
		Guardrails: NeMoGuardrailsConfig{
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
func (t *NeMoTransformer) TransformResponse(toolName string, content []byte, configIDs []string) ([]byte, error) {
	if toolName == "" {
		return nil, fmt.Errorf("nemo: tool name is required")
	}

	req := NeMoCheckRequest{
		Model: t.Model,
		Messages: []NeMoMessage{
			{
				Role:    "assistant",
				Name:    toolName,
				Content: string(content),
			},
		},
		Guardrails: NeMoGuardrailsConfig{
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
func (t *NeMoTransformer) ParseCheckResponse(body []byte) (*NeMoCheckResponse, error) {
	var resp NeMoCheckResponse
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

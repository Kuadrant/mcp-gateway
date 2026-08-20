package nemo

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNeMoTransformer_TransformRequest(t *testing.T) {
	t.Run("maps tool name, arguments, and config IDs", func(t *testing.T) {
		transformer := NewTransformer("meta/llama-3.1-8b-instruct")

		body, err := transformer.TransformRequest(
			"execute_sql",
			json.RawMessage(`{"query": "DROP TABLE users"}`),
			[]string{"tool-safety-v1", "input_checking"},
		)
		require.NoError(t, err)

		var got CheckRequest
		require.NoError(t, json.Unmarshal(body, &got))

		require.Equal(t, CheckRequest{
			Model: "meta/llama-3.1-8b-instruct",
			Messages: []Message{
				{
					Role:    "user",
					Name:    "execute_sql",
					Content: `{"query": "DROP TABLE users"}`,
					Config:  "tool",
				},
			},
			Guardrails: GuardrailsConfig{
				ConfigIDs: []string{"tool-safety-v1", "input_checking"},
			},
		}, got)
	})

	t.Run("defaults arguments to an empty object when absent", func(t *testing.T) {
		transformer := NewTransformer("meta/llama-3.1-8b-instruct")

		body, err := transformer.TransformRequest("no_args_tool", nil, nil)
		require.NoError(t, err)

		var got CheckRequest
		require.NoError(t, json.Unmarshal(body, &got))
		require.Equal(t, "{}", got.Messages[0].Content)
		require.Equal(t, []string{}, got.Guardrails.ConfigIDs)
	})

	t.Run("errors without a tool name", func(t *testing.T) {
		transformer := NewTransformer("meta/llama-3.1-8b-instruct")

		_, err := transformer.TransformRequest("", json.RawMessage(`{}`), nil)
		require.Error(t, err)
	})
}

func TestNeMoTransformer_TransformResponse(t *testing.T) {
	t.Run("maps tool name, text content, and config IDs with assistant role", func(t *testing.T) {
		transformer := NewTransformer("meta/llama-3.1-8b-instruct")

		body, err := transformer.TransformResponse(
			"execute_sql",
			[]byte("Query returned 42 rows. Customer emails: alice@example.com, bob@example.com"),
			[]string{"tool-safety-v1", "pii-detection"},
		)
		require.NoError(t, err)

		var got CheckRequest
		require.NoError(t, json.Unmarshal(body, &got))

		require.Equal(t, CheckRequest{
			Model: "meta/llama-3.1-8b-instruct",
			Messages: []Message{
				{
					Role:    "assistant",
					Name:    "execute_sql",
					Content: "Query returned 42 rows. Customer emails: alice@example.com, bob@example.com",
				},
			},
			Guardrails: GuardrailsConfig{
				ConfigIDs: []string{"tool-safety-v1", "pii-detection"},
			},
		}, got)
	})

	t.Run("does not set the tool config tag on response checks", func(t *testing.T) {
		transformer := NewTransformer("meta/llama-3.1-8b-instruct")

		body, err := transformer.TransformResponse("execute_sql", []byte("ok"), nil)
		require.NoError(t, err)
		require.NotContains(t, string(body), `"config"`)
	})

	t.Run("errors without a tool name", func(t *testing.T) {
		transformer := NewTransformer("meta/llama-3.1-8b-instruct")

		_, err := transformer.TransformResponse("", []byte("ok"), nil)
		require.Error(t, err)
	})
}

func TestNeMoTransformer_ParseCheckResponse(t *testing.T) {
	transformer := NewTransformer("meta/llama-3.1-8b-instruct")

	t.Run("parses a success verdict", func(t *testing.T) {
		resp, err := transformer.ParseCheckResponse([]byte(`{"status":"success","content":"ok","rail":null}`))
		require.NoError(t, err)
		require.Equal(t, &CheckResponse{Status: StatusSuccess, Content: "ok"}, resp)
	})

	t.Run("parses a modified verdict with the substituted content and triggering rail", func(t *testing.T) {
		resp, err := transformer.ParseCheckResponse([]byte(`{"status":"modified","content":"[REDACTED]","rail":"pii-detection"}`))
		require.NoError(t, err)
		require.Equal(t, &CheckResponse{Status: StatusModified, Content: "[REDACTED]", Rail: "pii-detection"}, resp)
	})

	t.Run("parses a blocked verdict", func(t *testing.T) {
		resp, err := transformer.ParseCheckResponse([]byte(`{"status":"blocked","content":"I can't share that.","rail":"pii-detection"}`))
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, resp.Status)
	})

	t.Run("errors on an unrecognized status", func(t *testing.T) {
		_, err := transformer.ParseCheckResponse([]byte(`{"status":"unknown"}`))
		require.Error(t, err)
	})

	t.Run("errors on malformed JSON", func(t *testing.T) {
		_, err := transformer.ParseCheckResponse([]byte(`not json`))
		require.Error(t, err)
	})
}

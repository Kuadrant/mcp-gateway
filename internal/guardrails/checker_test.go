package guardrails

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Kuadrant/mcp-gateway/internal/config"
)

func newTestChecker(t *testing.T, handler http.HandlerFunc, failMode string) Checker {
	t.Helper()
	return newTestCheckerWithMaxBodyBytes(t, handler, failMode, 0)
}

func newTestCheckerWithMaxBodyBytes(t *testing.T, handler http.HandlerFunc, failMode string, maxBodyBytes int64) Checker {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return NewChecker(&config.GuardrailsConfig{
		URL:       server.URL,
		Model:     "meta/llama-3.1-8b-instruct",
		ConfigIDs: []string{"global-1"},
		FailMode:  failMode,
	}, nil, 0, maxBodyBytes)
}

func TestNeMoChecker_CheckRequest(t *testing.T) {
	t.Run("success verdict is allowed", func(t *testing.T) {
		checker := newTestChecker(t, func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "/v1/guardrail/checks", r.URL.Path)

			messages := body["messages"].([]any)
			msg := messages[0].(map[string]any)
			require.Equal(t, "user", msg["role"])
			require.Equal(t, "execute_sql", msg["name"])

			guardrails := body["guardrails"].(map[string]any)
			require.Equal(t, []any{"global-1", "server-1"}, guardrails["config_ids"])

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"success","content":"ok"}`))
		}, FailModeDeny)

		decision, err := checker.CheckRequest(context.Background(), "execute_sql", json.RawMessage(`{"query":"SELECT 1"}`), []string{"server-1"})
		require.NoError(t, err)
		require.Equal(t, &Decision{Status: StatusAllowed, Content: "ok"}, decision)
	})

	t.Run("blocked verdict carries the triggering rail as reason", func(t *testing.T) {
		checker := newTestChecker(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"blocked","content":"denied","rail":"tool-safety-v1"}`))
		}, FailModeDeny)

		decision, err := checker.CheckRequest(context.Background(), "execute_sql", json.RawMessage(`{}`), nil)
		require.NoError(t, err)
		require.Equal(t, &Decision{Status: StatusBlocked, Content: "denied", Reason: "tool-safety-v1"}, decision)
	})

	t.Run("translation failure is a hard error regardless of failMode", func(t *testing.T) {
		checker := newTestChecker(t, func(_ http.ResponseWriter, _ *http.Request) {
			t.Fatal("guardrails server should not be called on translation failure")
		}, FailModeAllow)

		decision, err := checker.CheckRequest(context.Background(), "", json.RawMessage(`{}`), nil)
		require.Error(t, err)
		require.Nil(t, decision)
	})

	t.Run("non-2xx applies failMode: deny blocks", func(t *testing.T) {
		checker := newTestChecker(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}, FailModeDeny)

		decision, err := checker.CheckRequest(context.Background(), "execute_sql", json.RawMessage(`{}`), nil)
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, decision.Status)
		require.Error(t, decision.Err, "a failMode fallback must be distinguishable from a real blocked verdict")
	})

	t.Run("non-2xx applies failMode: allow releases", func(t *testing.T) {
		checker := newTestChecker(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}, FailModeAllow)

		decision, err := checker.CheckRequest(context.Background(), "execute_sql", json.RawMessage(`{}`), nil)
		require.NoError(t, err)
		require.Equal(t, StatusAllowed, decision.Status)
		require.Error(t, decision.Err)
	})

	t.Run("malformed guardrails response applies failMode rather than erroring", func(t *testing.T) {
		checker := newTestChecker(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`not json`))
		}, FailModeDeny)

		decision, err := checker.CheckRequest(context.Background(), "execute_sql", json.RawMessage(`{}`), nil)
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, decision.Status)
		require.Error(t, decision.Err)
	})

	t.Run("oversized guardrails response applies failMode rather than erroring", func(t *testing.T) {
		checker := newTestCheckerWithMaxBodyBytes(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"success","content":"` + strings.Repeat("a", 32) + `"}`))
		}, FailModeDeny, 8)

		decision, err := checker.CheckRequest(context.Background(), "execute_sql", json.RawMessage(`{}`), nil)
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, decision.Status)
		require.Error(t, decision.Err, "a failMode fallback must be distinguishable from a real blocked verdict")
	})

	t.Run("unreachable guardrails server applies failMode", func(t *testing.T) {
		checker := NewChecker(&config.GuardrailsConfig{
			URL:      "http://127.0.0.1:1",
			Model:    "meta/llama-3.1-8b-instruct",
			FailMode: FailModeAllow,
		}, nil, 0, 0)

		decision, err := checker.CheckRequest(context.Background(), "execute_sql", json.RawMessage(`{}`), nil)
		require.NoError(t, err)
		require.Equal(t, StatusAllowed, decision.Status)
		require.Error(t, decision.Err)
	})

	t.Run("a real blocked verdict carries no Err, unlike a failMode fallback", func(t *testing.T) {
		checker := newTestChecker(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"blocked","content":"denied","rail":"tool-safety-v1"}`))
		}, FailModeDeny)

		decision, err := checker.CheckRequest(context.Background(), "execute_sql", json.RawMessage(`{}`), nil)
		require.NoError(t, err)
		require.Equal(t, StatusBlocked, decision.Status)
		require.NoError(t, decision.Err)
	})
}

func TestNeMoChecker_CheckResponse(t *testing.T) {
	t.Run("modified verdict returns substituted content", func(t *testing.T) {
		checker := newTestChecker(t, func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			messages := body["messages"].([]any)
			msg := messages[0].(map[string]any)
			require.Equal(t, "assistant", msg["role"])
			_, hasConfig := msg["config"]
			require.False(t, hasConfig, "response checks must not set the tool config tag")

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"modified","content":"[REDACTED]","rail":"pii-detection"}`))
		}, FailModeDeny)

		decision, err := checker.CheckResponse(context.Background(), "execute_sql", []byte("alice@example.com"), nil)
		require.NoError(t, err)
		require.Equal(t, &Decision{Status: StatusModified, Content: "[REDACTED]", Reason: "pii-detection"}, decision)
	})
}

func TestMergeConfigIDs(t *testing.T) {
	require.Equal(t, []string{"a", "b"}, mergeConfigIDs([]string{"a"}, []string{"b"}))
	require.Equal(t, []string{"a"}, mergeConfigIDs([]string{"a"}, nil))
	require.Equal(t, []string{"b"}, mergeConfigIDs(nil, []string{"b"}))
	require.Nil(t, mergeConfigIDs(nil, nil))

	t.Run("deduplicates overlapping global and per-server IDs, keeping global order first", func(t *testing.T) {
		require.Equal(t, []string{"a", "b", "c"}, mergeConfigIDs([]string{"a", "b"}, []string{"b", "c"}))
	})

	t.Run("deduplicates within a single side", func(t *testing.T) {
		require.Equal(t, []string{"a", "b"}, mergeConfigIDs([]string{"a", "a"}, []string{"b", "b"}))
	})
}

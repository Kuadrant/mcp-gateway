package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/guardrails/api"
	"github.com/stretchr/testify/require"
)

// fakeChecker is a Checker test double that returns a canned
// Decision/error and records the last CheckRequest call for assertions.
type fakeChecker struct {
	decision *api.Decision
	err      error

	calls         int
	lastToolName  string
	lastArguments json.RawMessage
	lastConfigIDs []string
}

func (f *fakeChecker) CheckRequest(_ context.Context, toolName string, arguments json.RawMessage, configIDs []string) (*api.Decision, error) {
	f.calls++
	f.lastToolName = toolName
	f.lastArguments = arguments
	f.lastConfigIDs = configIDs
	return f.decision, f.err
}

func (f *fakeChecker) CheckResponse(context.Context, string, []byte, []string) (*api.Decision, error) {
	return f.decision, f.err
}

func (f *fakeChecker) Close() error { return nil }

var errTranslation = errors.New("guardrails: request translation failed")

func TestCheckGuardrailsRequest(t *testing.T) {
	args := json.RawMessage(`{"key":"value"}`)
	globalCfg := &api.Config{ConfigIDs: []string{"global-1"}, FailMode: api.FailModeDeny}

	t.Run("nil checker with no IDs skips check", func(t *testing.T) {
		gc := guardrailsCheck{buildError: BuildSSEJSONRPCError}
		modified, decision := gc.request(context.Background(), "mytool", args, 1)
		require.Nil(t, decision)
		require.Empty(t, modified)
	})

	t.Run("empty merged config IDs skips check without calling checker", func(t *testing.T) {
		fc := &fakeChecker{}
		gc := guardrailsCheck{checker: fc, global: &api.Config{}, buildError: BuildSSEJSONRPCError}
		modified, decision := gc.request(context.Background(), "mytool", args, 1)
		require.Nil(t, decision)
		require.Empty(t, modified)
		require.Equal(t, 0, fc.calls, "checker must not be called when merged config IDs are empty")
	})

	t.Run("non-empty IDs with nil checker fail closed", func(t *testing.T) {
		gc := guardrailsCheck{global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		_, decision := gc.request(context.Background(), "mytool", args, 1)
		require.NotNil(t, decision)
		require.Equal(t, 503, decision.Error.StatusCode)
		require.Contains(t, decision.Error.JSONRPCErr, `"error"`)
		require.NotContains(t, decision.Error.JSONRPCErr, "isError")
	})

	t.Run("nil global guardrails config with server IDs still checks", func(t *testing.T) {
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusAllowed}}
		gc := guardrailsCheck{checker: fc, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		_, decision := gc.request(context.Background(), "mytool", args, 1)
		require.Nil(t, decision)
		require.Equal(t, 1, fc.calls)
		require.Equal(t, []string{"svr-1"}, fc.lastConfigIDs)
	})

	t.Run("allowed proceeds with normal routing", func(t *testing.T) {
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusAllowed}}
		gc := guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		modified, decision := gc.request(context.Background(), "mytool", args, 1)
		require.Nil(t, decision)
		require.Empty(t, modified)
		require.Equal(t, "mytool", fc.lastToolName)
		require.JSONEq(t, `{"key":"value"}`, string(fc.lastArguments))
		require.Equal(t, []string{"svr-1"}, fc.lastConfigIDs)
	})

	t.Run("modified returns rewritten content", func(t *testing.T) {
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusModified, Content: `{"key":"redacted"}`}}
		gc := guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		modified, decision := gc.request(context.Background(), "mytool", args, 1)
		require.Nil(t, decision)
		require.Equal(t, `{"key":"redacted"}`, modified)
	})

	t.Run("blocked returns 403 with a generic JSON-RPC error, never the triggering reason", func(t *testing.T) {
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusBlocked, Reason: "pii"}}
		gc := guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		_, decision := gc.request(context.Background(), "mytool", args, 1)
		require.NotNil(t, decision)
		require.NotNil(t, decision.Error)
		require.Equal(t, 403, decision.Error.StatusCode)
		require.Contains(t, decision.Error.JSONRPCErr, guardrailsBlockedMessage)
		require.NotContains(t, decision.Error.JSONRPCErr, "pii", "the triggering rail must not reach the client")
		require.Contains(t, decision.Error.JSONRPCErr, `"error"`)
		require.NotContains(t, decision.Error.JSONRPCErr, "isError")
	})

	t.Run("blocked logs the triggering reason for operators", func(t *testing.T) {
		var buf bytes.Buffer
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusBlocked, Reason: "pii-detection"}}
		gc := guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, logger: slog.New(slog.NewTextHandler(&buf, nil)), buildError: BuildSSEJSONRPCError}
		_, decision := gc.request(context.Background(), "mytool", args, 1)
		require.NotNil(t, decision)
		require.NotContains(t, decision.Error.JSONRPCErr, "pii-detection")
		require.Contains(t, buf.String(), "pii-detection")
	})

	t.Run("blocked via failMode deny fallback returns 503", func(t *testing.T) {
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusBlocked, Err: context.DeadlineExceeded}}
		gc := guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		_, decision := gc.request(context.Background(), "mytool", args, 1)
		require.NotNil(t, decision)
		require.NotNil(t, decision.Error)
		require.Equal(t, 503, decision.Error.StatusCode)
	})

	t.Run("allowed via failMode allow fallback proceeds", func(t *testing.T) {
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusAllowed, Err: context.DeadlineExceeded}}
		gc := guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		_, decision := gc.request(context.Background(), "mytool", args, 1)
		require.Nil(t, decision)
	})

	t.Run("failMode allow logs the underlying error", func(t *testing.T) {
		var buf bytes.Buffer
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusAllowed, Err: context.DeadlineExceeded}}
		gc := guardrailsCheck{
			checker:    fc,
			global:     globalCfg,
			serverIDs:  []string{"svr-1"},
			logger:     slog.New(slog.NewTextHandler(&buf, nil)),
			buildError: BuildSSEJSONRPCError,
		}
		_, decision := gc.request(context.Background(), "mytool", args, 1)
		require.Nil(t, decision)
		require.Contains(t, buf.String(), "guardrails check failed open")
		require.Contains(t, buf.String(), context.DeadlineExceeded.Error())
	})

	t.Run("nil decision returns 400", func(t *testing.T) {
		gc := guardrailsCheck{checker: &fakeChecker{}, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		_, decision := gc.request(context.Background(), "mytool", args, 1)
		require.NotNil(t, decision)
		require.Equal(t, 400, decision.Error.StatusCode)
	})

	t.Run("translation error returns 400 with a generic message, never the internal error", func(t *testing.T) {
		gc := guardrailsCheck{checker: &fakeChecker{err: errTranslation}, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		_, decision := gc.request(context.Background(), "mytool", args, 1)
		require.NotNil(t, decision)
		require.NotNil(t, decision.Error)
		require.Equal(t, 400, decision.Error.StatusCode)
		require.Contains(t, decision.Error.JSONRPCErr, guardrailsCheckFailedMessage)
		require.NotContains(t, decision.Error.JSONRPCErr, errTranslation.Error())
	})

	t.Run("translation error logs the internal detail for operators", func(t *testing.T) {
		var buf bytes.Buffer
		gc := guardrailsCheck{
			checker:    &fakeChecker{err: errTranslation},
			global:     globalCfg,
			serverIDs:  []string{"svr-1"},
			logger:     slog.New(slog.NewTextHandler(&buf, nil)),
			buildError: BuildSSEJSONRPCError,
		}
		_, decision := gc.request(context.Background(), "mytool", args, 1)
		require.NotNil(t, decision)
		require.NotContains(t, decision.Error.JSONRPCErr, errTranslation.Error())
		require.Contains(t, buf.String(), errTranslation.Error())
	})
}

func TestToolCallArguments(t *testing.T) {
	t.Run("marshals params.arguments", func(t *testing.T) {
		raw, err := toolCallArguments(map[string]any{"name": "mytool", "arguments": map[string]any{"q": 1}})
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		require.Equal(t, float64(1), got["q"])
	})

	t.Run("missing arguments yields empty object", func(t *testing.T) {
		raw, err := toolCallArguments(map[string]any{"name": "mytool"})
		require.NoError(t, err)
		require.Equal(t, `{}`, string(raw))
	})

	t.Run("nil params yields empty object", func(t *testing.T) {
		raw, err := toolCallArguments(nil)
		require.NoError(t, err)
		require.Equal(t, `{}`, string(raw))
	})
}

func TestElicitationArguments(t *testing.T) {
	t.Run("strips action and keeps the rest", func(t *testing.T) {
		raw, err := elicitationArguments(map[string]any{"action": "accept", "content": map[string]any{"name": "test"}})
		require.NoError(t, err)
		restored := map[string]any{}
		require.NoError(t, json.Unmarshal(raw, &restored))
		content, ok := restored["content"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, "test", content["name"])
		_, hasAction := restored["action"]
		require.False(t, hasAction)
	})

	t.Run("action only yields empty object", func(t *testing.T) {
		raw, err := elicitationArguments(map[string]any{"action": "accept"})
		require.NoError(t, err)
		require.Equal(t, `{}`, string(raw))
	})
}

func TestIsElicitationAccept(t *testing.T) {
	require.True(t, isElicitationAccept(&MCPRequest{Result: map[string]any{"action": "accept"}}))
	require.False(t, isElicitationAccept(&MCPRequest{Result: map[string]any{"action": "decline"}}))
	require.False(t, isElicitationAccept(&MCPRequest{Result: map[string]any{"action": "cancel"}}))
	require.False(t, isElicitationAccept(&MCPRequest{Method: "tools/call"}))
}

func TestGuardrailsArguments(t *testing.T) {
	t.Run("nil request skips", func(t *testing.T) {
		gc := &guardrailsCheck{buildError: BuildSSEJSONRPCError}
		modified, blocked := gc.checkToolCall(context.Background(), nil, "mytool")
		require.False(t, modified)
		require.Nil(t, blocked)
	})

	t.Run("arguments that cannot marshal returns 400", func(t *testing.T) {
		badReq := &MCPRequest{
			Method: MethodToolCall,
			Params: map[string]any{"name": "mytool", "arguments": make(chan int)},
		}
		gc := &guardrailsCheck{buildError: BuildSSEJSONRPCError}
		modified, blocked := gc.checkToolCall(context.Background(), badReq, "mytool")
		require.False(t, modified)
		require.NotNil(t, blocked)
		require.Equal(t, 400, blocked.Error.StatusCode)
		require.Contains(t, blocked.Error.JSONRPCErr, guardrailsCheckFailedMessage)
	})
}

func TestCheckToolCall_Modified(t *testing.T) {
	fc := &fakeChecker{decision: &api.Decision{Status: api.StatusModified, Content: `{"q":"redacted"}`}}
	gc := &guardrailsCheck{
		checker:    fc,
		serverIDs:  []string{"svr-1"},
		buildError: BuildSSEJSONRPCError,
	}
	req := &MCPRequest{
		ID:     1,
		Method: MethodToolCall,
		Params: map[string]any{"name": "mytool", "arguments": map[string]any{"q": "secret"}},
	}
	modified, blocked := gc.checkToolCall(context.Background(), req, "mytool")
	require.Nil(t, blocked)
	require.True(t, modified)
	require.Equal(t, map[string]any{"q": "redacted"}, req.Params["arguments"])
}

func TestNewGuardrailsCheck_Options(t *testing.T) {
	fc := &fakeChecker{decision: &api.Decision{Status: api.StatusAllowed}}
	cfg := &config.MCPServersConfig{}
	cfg.ApplyReload([]*config.MCPServer{{
		Name:                "dummy",
		GuardrailsConfigIDs: []string{"svr-1"},
	}}, nil, "", 0, nil, fc)

	t.Run("defaults to JSON errors", func(t *testing.T) {
		gc := newGuardrailsCheck(cfg, []string{"svr-1"}, nil)
		require.Equal(t, "application/json", gc.contentType)
		require.Equal(t, []string{"svr-1"}, gc.serverIDs)
	})

	t.Run("withSSEErrors overrides defaults", func(t *testing.T) {
		gc := newGuardrailsCheck(cfg, []string{"svr-1"}, nil, withSSEErrors())
		require.Empty(t, gc.contentType)
		require.Equal(t, []string{"svr-1"}, gc.serverIDs)
	})
}

func TestCheckToolCallResponse(t *testing.T) {
	text := []byte("some tool output")
	globalCfg := &api.Config{ConfigIDs: []string{"global-1"}}

	t.Run("nil text skips check", func(t *testing.T) {
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusBlocked}}
		gc := &guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		result := gc.checkToolCallResponse(context.Background(), "mytool", nil, 1, BuildSSEToolError, BuildSSEToolResult)
		require.Nil(t, result)
		require.Equal(t, 0, fc.calls)
	})

	t.Run("empty merged config IDs skips check", func(t *testing.T) {
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusBlocked}}
		gc := &guardrailsCheck{checker: fc, buildError: BuildSSEJSONRPCError}
		result := gc.checkToolCallResponse(context.Background(), "mytool", text, 1, BuildSSEToolError, BuildSSEToolResult)
		require.Nil(t, result)
		require.Equal(t, 0, fc.calls)
	})

	t.Run("allowed returns nil (pass through original)", func(t *testing.T) {
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusAllowed}}
		gc := &guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		result := gc.checkToolCallResponse(context.Background(), "mytool", text, 1, BuildSSEToolError, BuildSSEToolResult)
		require.Nil(t, result)
	})

	t.Run("blocked returns isError tool result, not a top-level JSON-RPC error", func(t *testing.T) {
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusBlocked, Reason: "pii"}}
		gc := &guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		result := gc.checkToolCallResponse(context.Background(), "mytool", text, 1, BuildSSEToolError, BuildSSEToolResult)
		require.NotNil(t, result)
		body := string(result)
		require.Contains(t, body, `"isError":true`)
		require.Contains(t, body, guardrailsBlockedMessage)
		require.NotContains(t, body, "pii", "triggering reason must not reach the client")
		require.NotContains(t, body, `"error":{`, "must be an isError tool result, not a top-level JSON-RPC error")
	})

	t.Run("modified returns successful result with redacted content, not isError", func(t *testing.T) {
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusModified, Content: "redacted output"}}
		gc := &guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		result := gc.checkToolCallResponse(context.Background(), "mytool", text, 1, BuildSSEToolError, BuildSSEToolResult)
		require.NotNil(t, result)
		body := string(result)
		require.Contains(t, body, "redacted output")
		require.NotContains(t, body, `"isError"`, "StatusModified is a successful redacted result, not a tool failure")
	})

	t.Run("nil checker with IDs fails closed and returns isError body", func(t *testing.T) {
		gc := &guardrailsCheck{global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		result := gc.checkToolCallResponse(context.Background(), "mytool", text, 1, BuildSSEToolError, BuildSSEToolResult)
		require.NotNil(t, result)
		require.Contains(t, string(result), `"isError":true`)
		// checker unavailable is distinct from a policy block so operators can diagnose connectivity issues
		require.Contains(t, string(result), guardrailsUnavailableMessage)
	})

	t.Run("translation error fails closed and returns isError body", func(t *testing.T) {
		fc := &fakeChecker{err: errTranslation}
		gc := &guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		result := gc.checkToolCallResponse(context.Background(), "mytool", text, 1, BuildSSEToolError, BuildSSEToolResult)
		require.NotNil(t, result)
		require.Contains(t, string(result), `"isError":true`)
		require.NotContains(t, string(result), errTranslation.Error(), "internal error must not reach the client")
		// translation errors use guardrailsCheckFailedMessage, not guardrailsBlockedMessage
		require.Contains(t, string(result), guardrailsCheckFailedMessage)
	})

	t.Run("blocked reason not leaked to client", func(t *testing.T) {
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusBlocked, Reason: "credit-card-detection"}}
		gc := &guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		result := gc.checkToolCallResponse(context.Background(), "mytool", text, 1, BuildSSEToolError, BuildSSEToolResult)
		require.NotNil(t, result)
		require.NotContains(t, string(result), "credit-card-detection")
	})

	t.Run("uses SSE format when BuildSSEToolError passed", func(t *testing.T) {
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusBlocked}}
		gc := &guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		result := gc.checkToolCallResponse(context.Background(), "mytool", text, 1, BuildSSEToolError, BuildSSEToolResult)
		require.NotNil(t, result)
		require.Contains(t, string(result), "event: message")
		require.Contains(t, string(result), "data: ")
	})

	t.Run("modified returns successful result with redacted content, not isError", func(t *testing.T) {
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusModified, Content: "safe text"}}
		gc := &guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		result := gc.checkToolCallResponse(context.Background(), "mytool", text, 1, BuildSSEToolError, BuildSSEToolResult)
		require.NotNil(t, result)
		body := string(result)
		require.Contains(t, body, "event: message")
		require.Contains(t, body, "safe text")
		require.NotContains(t, body, `"isError"`)
	})

	t.Run("modified with empty content returns successful empty result, not pass-through", func(t *testing.T) {
		// checker returns StatusModified with empty Content — means "redact everything".
		// must NOT be treated as StatusAllowed (pass-through), which would forward original.
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusModified, Content: ""}}
		gc := &guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		result := gc.checkToolCallResponse(context.Background(), "mytool", text, 1, BuildSSEToolError, BuildSSEToolResult)
		require.NotNil(t, result, "StatusModified with empty content must not be treated as pass-through")
		require.NotContains(t, string(result), `"isError"`, "empty modified is a successful result, not a tool error")
	})

	t.Run("blocked with Err returns guardrailsUnavailableMessage, not guardrailsBlockedMessage", func(t *testing.T) {
		fc := &fakeChecker{decision: &api.Decision{Status: api.StatusBlocked, Err: fmt.Errorf("nemo unreachable")}}
		gc := &guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		result := gc.checkToolCallResponse(context.Background(), "mytool", text, 1, BuildSSEToolError, BuildSSEToolResult)
		require.NotNil(t, result)
		body := string(result)
		require.Contains(t, body, guardrailsUnavailableMessage, "provider error must surface as unavailable, not blocked")
		require.NotContains(t, body, guardrailsBlockedMessage)
		require.NotContains(t, body, "nemo unreachable", "provider error must not reach the client")
	})
}

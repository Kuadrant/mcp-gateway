package routing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/guardrails"
	"github.com/stretchr/testify/require"
)

// fakeChecker is a Checker test double that returns a canned
// Decision/error and records the last CheckRequest call for assertions.
type fakeChecker struct {
	decision *GuardrailsDecision
	err      error

	calls         int
	lastToolName  string
	lastArguments json.RawMessage
	lastConfigIDs []string
}

func (f *fakeChecker) CheckRequest(_ context.Context, toolName string, arguments json.RawMessage, configIDs []string) (*GuardrailsDecision, error) {
	f.calls++
	f.lastToolName = toolName
	f.lastArguments = arguments
	f.lastConfigIDs = configIDs
	return f.decision, f.err
}

func (f *fakeChecker) CheckResponse(context.Context, string, []byte, []string) (*GuardrailsDecision, error) {
	return f.decision, f.err
}

var errTranslation = errors.New("guardrails: request translation failed")

func TestCheckGuardrailsRequest(t *testing.T) {
	args := json.RawMessage(`{"key":"value"}`)
	globalCfg := &guardrails.Config{ConfigIDs: []string{"global-1"}, FailMode: guardrails.FailModeDeny}

	t.Run("nil checker with no IDs skips check", func(t *testing.T) {
		gc := guardrailsCheck{buildError: BuildSSEJSONRPCError}
		modified, decision := gc.request(context.Background(), "mytool", args, 1)
		require.Nil(t, decision)
		require.Empty(t, modified)
	})

	t.Run("empty merged config IDs skips check without calling checker", func(t *testing.T) {
		fc := &fakeChecker{}
		gc := guardrailsCheck{checker: fc, global: &guardrails.Config{}, buildError: BuildSSEJSONRPCError}
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
		fc := &fakeChecker{decision: &GuardrailsDecision{Status: StatusAllowed}}
		gc := guardrailsCheck{checker: fc, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		_, decision := gc.request(context.Background(), "mytool", args, 1)
		require.Nil(t, decision)
		require.Equal(t, 1, fc.calls)
		require.Equal(t, []string{"svr-1"}, fc.lastConfigIDs)
	})

	t.Run("allowed proceeds with normal routing", func(t *testing.T) {
		fc := &fakeChecker{decision: &GuardrailsDecision{Status: StatusAllowed}}
		gc := guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		modified, decision := gc.request(context.Background(), "mytool", args, 1)
		require.Nil(t, decision)
		require.Empty(t, modified)
		require.Equal(t, "mytool", fc.lastToolName)
		require.JSONEq(t, `{"key":"value"}`, string(fc.lastArguments))
		require.Equal(t, []string{"svr-1"}, fc.lastConfigIDs)
	})

	t.Run("modified returns rewritten content", func(t *testing.T) {
		fc := &fakeChecker{decision: &GuardrailsDecision{Status: StatusModified, Content: `{"key":"redacted"}`}}
		gc := guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		modified, decision := gc.request(context.Background(), "mytool", args, 1)
		require.Nil(t, decision)
		require.Equal(t, `{"key":"redacted"}`, modified)
	})

	t.Run("blocked returns 403 with a generic JSON-RPC error, never the triggering reason", func(t *testing.T) {
		fc := &fakeChecker{decision: &GuardrailsDecision{Status: StatusBlocked, Reason: "pii"}}
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
		fc := &fakeChecker{decision: &GuardrailsDecision{Status: StatusBlocked, Reason: "pii-detection"}}
		gc := guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, logger: slog.New(slog.NewTextHandler(&buf, nil)), buildError: BuildSSEJSONRPCError}
		_, decision := gc.request(context.Background(), "mytool", args, 1)
		require.NotNil(t, decision)
		require.NotContains(t, decision.Error.JSONRPCErr, "pii-detection")
		require.Contains(t, buf.String(), "pii-detection")
	})

	t.Run("blocked via failMode deny fallback returns 503", func(t *testing.T) {
		fc := &fakeChecker{decision: &GuardrailsDecision{Status: StatusBlocked, Err: context.DeadlineExceeded}}
		gc := guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		_, decision := gc.request(context.Background(), "mytool", args, 1)
		require.NotNil(t, decision)
		require.NotNil(t, decision.Error)
		require.Equal(t, 503, decision.Error.StatusCode)
	})

	t.Run("allowed via failMode allow fallback proceeds", func(t *testing.T) {
		fc := &fakeChecker{decision: &GuardrailsDecision{Status: StatusAllowed, Err: context.DeadlineExceeded}}
		gc := guardrailsCheck{checker: fc, global: globalCfg, serverIDs: []string{"svr-1"}, buildError: BuildSSEJSONRPCError}
		_, decision := gc.request(context.Background(), "mytool", args, 1)
		require.Nil(t, decision)
	})

	t.Run("failMode allow logs the underlying error", func(t *testing.T) {
		var buf bytes.Buffer
		fc := &fakeChecker{decision: &GuardrailsDecision{Status: StatusAllowed, Err: context.DeadlineExceeded}}
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
	fc := &fakeChecker{decision: &GuardrailsDecision{Status: StatusModified, Content: `{"q":"redacted"}`}}
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
	fc := &fakeChecker{decision: &GuardrailsDecision{Status: StatusAllowed}}
	cfg := &config.MCPServersConfig{}
	cfg.ApplyReload([]*config.MCPServer{{
		Name:                "dummy",
		GuardrailsConfigIDs: []string{"svr-1"},
	}}, nil, "", 0, nil, fc)

	t.Run("defaults to JSON errors", func(t *testing.T) {
		gc := newGuardrailsCheck(cfg, "dummy", nil)
		require.Equal(t, "application/json", gc.contentType)
		require.Equal(t, []string{"svr-1"}, gc.serverIDs)
		require.NotNil(t, gc.server)
	})

	t.Run("withSSEErrors overrides defaults", func(t *testing.T) {
		gc := newGuardrailsCheck(cfg, "dummy", nil, withSSEErrors())
		require.Empty(t, gc.contentType)
		require.Equal(t, []string{"svr-1"}, gc.serverIDs)
	})
}

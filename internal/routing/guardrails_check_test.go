package routing

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/guardrails"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

// fakeChecker is a guardrails.Checker test double that returns a canned
// Decision/error and records the last CheckRequest call for assertions.
type fakeChecker struct {
	decision *guardrails.Decision
	err      error

	calls         int
	lastToolName  string
	lastArguments json.RawMessage
	lastConfigIDs []string
}

func (f *fakeChecker) CheckRequest(_ context.Context, toolName string, arguments json.RawMessage, configIDs []string) (*guardrails.Decision, error) {
	f.calls++
	f.lastToolName = toolName
	f.lastArguments = arguments
	f.lastConfigIDs = configIDs
	return f.decision, f.err
}

func (f *fakeChecker) CheckResponse(context.Context, string, []byte, []string) (*guardrails.Decision, error) {
	return f.decision, f.err
}

var errTranslation = errors.New("guardrails: request translation failed")

func TestCheckGuardrailsRequest(t *testing.T) {
	args := json.RawMessage(`{"key":"value"}`)
	globalCfg := &config.GuardrailsConfig{ConfigIDs: []string{"global-1"}, FailMode: guardrails.FailModeDeny}

	newSpan := func(t *testing.T) trace.Span {
		t.Helper()
		_, span := tracer().Start(context.Background(), "test")
		t.Cleanup(func() { span.End() })
		return span
	}

	t.Run("nil checker func with no IDs skips check", func(t *testing.T) {
		modified, decision := checkGuardrailsRequest(context.Background(), newSpan(t), nil, nil, nil, nil, "mytool", args, 1, BuildSSEJSONRPCError, "")
		require.Nil(t, decision)
		require.Empty(t, modified)
	})

	t.Run("empty merged config IDs skips check without calling checker", func(t *testing.T) {
		fc := &fakeChecker{}
		checkerFn := func() guardrails.Checker { return fc }
		modified, decision := checkGuardrailsRequest(context.Background(), newSpan(t), nil, checkerFn, &config.GuardrailsConfig{}, nil, "mytool", args, 1, BuildSSEJSONRPCError, "")
		require.Nil(t, decision)
		require.Empty(t, modified)
		require.Equal(t, 0, fc.calls, "checker must not be called when merged config IDs are empty")
	})

	t.Run("non-empty IDs with nil checker fail closed", func(t *testing.T) {
		_, decision := checkGuardrailsRequest(context.Background(), newSpan(t), nil, nil, globalCfg, []string{"svr-1"}, "mytool", args, 1, BuildSSEJSONRPCError, "")
		require.NotNil(t, decision)
		require.Equal(t, 503, decision.Error.StatusCode)
		require.Contains(t, decision.Error.JSONRPCErr, `"error"`)
		require.NotContains(t, decision.Error.JSONRPCErr, "isError")
	})

	t.Run("nil global guardrails config with server IDs still checks", func(t *testing.T) {
		fc := &fakeChecker{decision: &guardrails.Decision{Status: guardrails.StatusAllowed}}
		checkerFn := func() guardrails.Checker { return fc }
		_, decision := checkGuardrailsRequest(context.Background(), newSpan(t), nil, checkerFn, nil, []string{"svr-1"}, "mytool", args, 1, BuildSSEJSONRPCError, "")
		require.Nil(t, decision)
		require.Equal(t, 1, fc.calls)
		require.Equal(t, []string{"svr-1"}, fc.lastConfigIDs)
	})

	t.Run("allowed proceeds with normal routing", func(t *testing.T) {
		fc := &fakeChecker{decision: &guardrails.Decision{Status: guardrails.StatusAllowed}}
		checkerFn := func() guardrails.Checker { return fc }
		modified, decision := checkGuardrailsRequest(context.Background(), newSpan(t), nil, checkerFn, globalCfg, []string{"svr-1"}, "mytool", args, 1, BuildSSEJSONRPCError, "")
		require.Nil(t, decision)
		require.Empty(t, modified)
		require.Equal(t, "mytool", fc.lastToolName)
		require.JSONEq(t, `{"key":"value"}`, string(fc.lastArguments))
		require.Equal(t, []string{"svr-1"}, fc.lastConfigIDs)
	})

	t.Run("modified returns rewritten content", func(t *testing.T) {
		fc := &fakeChecker{decision: &guardrails.Decision{Status: guardrails.StatusModified, Content: `{"key":"redacted"}`}}
		checkerFn := func() guardrails.Checker { return fc }
		modified, decision := checkGuardrailsRequest(context.Background(), newSpan(t), nil, checkerFn, globalCfg, []string{"svr-1"}, "mytool", args, 1, BuildSSEJSONRPCError, "")
		require.Nil(t, decision)
		require.Equal(t, `{"key":"redacted"}`, modified)
	})

	t.Run("blocked returns 403 with JSON-RPC error object", func(t *testing.T) {
		fc := &fakeChecker{decision: &guardrails.Decision{Status: guardrails.StatusBlocked, Reason: "pii"}}
		checkerFn := func() guardrails.Checker { return fc }
		_, decision := checkGuardrailsRequest(context.Background(), newSpan(t), nil, checkerFn, globalCfg, []string{"svr-1"}, "mytool", args, 1, BuildSSEJSONRPCError, "")
		require.NotNil(t, decision)
		require.NotNil(t, decision.Error)
		require.Equal(t, 403, decision.Error.StatusCode)
		require.Contains(t, decision.Error.JSONRPCErr, "pii")
		require.Contains(t, decision.Error.JSONRPCErr, `"error"`)
		require.NotContains(t, decision.Error.JSONRPCErr, "isError")
	})

	t.Run("blocked via failMode deny fallback returns 503", func(t *testing.T) {
		fc := &fakeChecker{decision: &guardrails.Decision{Status: guardrails.StatusBlocked, Err: context.DeadlineExceeded}}
		checkerFn := func() guardrails.Checker { return fc }
		_, decision := checkGuardrailsRequest(context.Background(), newSpan(t), nil, checkerFn, globalCfg, []string{"svr-1"}, "mytool", args, 1, BuildSSEJSONRPCError, "")
		require.NotNil(t, decision)
		require.NotNil(t, decision.Error)
		require.Equal(t, 503, decision.Error.StatusCode)
	})

	t.Run("allowed via failMode allow fallback proceeds", func(t *testing.T) {
		fc := &fakeChecker{decision: &guardrails.Decision{Status: guardrails.StatusAllowed, Err: context.DeadlineExceeded}}
		checkerFn := func() guardrails.Checker { return fc }
		_, decision := checkGuardrailsRequest(context.Background(), newSpan(t), nil, checkerFn, globalCfg, []string{"svr-1"}, "mytool", args, 1, BuildSSEJSONRPCError, "")
		require.Nil(t, decision)
	})

	t.Run("translation error returns 400 regardless of failMode", func(t *testing.T) {
		checkerFn := func() guardrails.Checker { return &fakeChecker{err: errTranslation} }
		_, decision := checkGuardrailsRequest(context.Background(), newSpan(t), nil, checkerFn, globalCfg, []string{"svr-1"}, "mytool", args, 1, BuildSSEJSONRPCError, "")
		require.NotNil(t, decision)
		require.NotNil(t, decision.Error)
		require.Equal(t, 400, decision.Error.StatusCode)
	})
}

func TestGuardrailsArguments(t *testing.T) {
	t.Run("nil request skips", func(t *testing.T) {
		args, blocked := guardrailsArguments(nil, 1, BuildSSEJSONRPCError, "")
		require.Nil(t, args)
		require.Nil(t, blocked)
	})

	t.Run("unmarshalable arguments returns 400", func(t *testing.T) {
		badReq := &MCPRequest{
			Method: MethodToolCall,
			Params: map[string]any{"name": "mytool", "arguments": make(chan int)},
		}
		args, blocked := guardrailsArguments(badReq, 1, BuildSSEJSONRPCError, "")
		require.Nil(t, args)
		require.NotNil(t, blocked)
		require.Equal(t, 400, blocked.Error.StatusCode)
	})
}

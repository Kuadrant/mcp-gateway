package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/guardrails"
)

// Checker runs guardrails checks against tools/call requests and responses.
type Checker = guardrails.Checker

// Status is the outcome of a guardrails check.
type Status = guardrails.Status

// GuardrailsDecision is the outcome of a single guardrails check.
type GuardrailsDecision = guardrails.Decision

// Status values a GuardrailsDecision can carry.
const (
	StatusAllowed  = guardrails.StatusAllowed
	StatusBlocked  = guardrails.StatusBlocked
	StatusModified = guardrails.StatusModified
)

// guardrailsToolErrorBuilder builds the transport-specific JSON-RPC error
// object for a blocked request: BuildSSEJSONRPCError for 2025-11-25,
// BuildJSONRPCError for 2026-07-28.
type guardrailsToolErrorBuilder func(requestID any, message string) string

const (
	guardrailsUnavailableMessage = "guardrails check unavailable"
	guardrailsBlockedMessage     = "blocked by guardrails"
	guardrailsCheckFailedMessage = "guardrails check failed"
)

func loadedGuardrails(cfg *config.MCPServersConfig) (Checker, *guardrails.Config) {
	if cfg == nil {
		return nil, nil
	}
	return cfg.GetGuardrails()
}

// guardrailsCheck runs guardrails checks for one routing decision: skip,
// fail-closed, call Checker, map the verdict onto a routing Decision. 
type guardrailsCheck struct {
	checker     Checker
	global      *guardrails.Config
	logger      *slog.Logger
	buildError  guardrailsToolErrorBuilder
	contentType string
}

// newGuardrailsCheck loads the current Checker and global config from cfg
// and pairs them with the transport's error builder. logger may be nil.
func newGuardrailsCheck(cfg *config.MCPServersConfig, logger *slog.Logger, buildError guardrailsToolErrorBuilder, contentType string) guardrailsCheck {
	checker, global := loadedGuardrails(cfg)
	return guardrailsCheck{checker: checker, global: global, logger: logger, buildError: buildError, contentType: contentType}
}

// request runs the guardrails check for name/arguments. blocked is non-nil
// when the request must not proceed. modified is the guardrails-rewritten
// content when the verdict is StatusModified; callers must apply it before
// forwarding. Empty merged config IDs skip the check entirely. Non-empty
// IDs with no Checker fail closed (503).
func (g guardrailsCheck) request(ctx context.Context, serverConfigIDs []string, name string, arguments json.RawMessage, requestID any) (modified string, blocked *Decision) {
	var globalConfigIDs []string
	if g.global != nil {
		globalConfigIDs = g.global.ConfigIDs
	}
	if len(globalConfigIDs) == 0 && len(serverConfigIDs) == 0 {
		return "", nil
	}

	if g.checker == nil {
		return "", g.errorDecision(503, requestID, guardrailsUnavailableMessage)
	}

	decision, checkErr := g.checker.CheckRequest(ctx, name, arguments, serverConfigIDs)
	if checkErr != nil {
		// translation failure only; always a hard deny, failMode does not
		// apply. checkErr can carry internal transport/provider detail, so
		// it's logged rather than returned to the client.
		g.logError(ctx, "guardrails request translation failed", name, checkErr)
		return "", g.errorDecision(400, requestID, guardrailsCheckFailedMessage)
	}

	switch decision.Status {
	case StatusBlocked:
		if decision.Err != nil {
			g.logError(ctx, "guardrails check unavailable, failing closed", name, decision.Err)
			return "", g.errorDecision(503, requestID, guardrailsUnavailableMessage)
		}
		if g.logger != nil {
			g.logger.InfoContext(ctx, "guardrails blocked request", "tool", name, "reason", decision.Reason)
		}
		return "", g.errorDecision(403, requestID, guardrailsBlockedMessage)
	case StatusAllowed:
		return "", nil
	case StatusModified:
		return decision.Content, nil
	default:
		g.logError(ctx, "guardrails returned unrecognized status", name, fmt.Errorf("status %q", decision.Status))
		return "", g.errorDecision(400, requestID, guardrailsCheckFailedMessage)
	}
}

func (g guardrailsCheck) errorDecision(status int, requestID any, message string) *Decision {
	return jsonRPCErrorDecision(status, requestID, message, g.buildError, g.contentType)
}

func (g guardrailsCheck) logError(ctx context.Context, msg, toolName string, err error) {
	if g.logger == nil {
		return
	}
	g.logger.ErrorContext(ctx, msg, "tool", toolName, "error", err)
}

func jsonRPCErrorDecision(status int, requestID any, message string, build guardrailsToolErrorBuilder, contentType string) *Decision {
	return &Decision{
		Error: &Error{
			StatusCode:  status,
			JSONRPCErr:  build(requestID, message),
			ContentType: contentType,
		},
	}
}

func guardrailsArguments(mcpReq *MCPRequest, requestID any, buildToolError guardrailsToolErrorBuilder, contentType string) (json.RawMessage, *Decision) {
	if mcpReq == nil {
		return nil, nil
	}
	args, err := toolCallArguments(mcpReq.Params)
	if err != nil {
		return nil, jsonRPCErrorDecision(400, requestID, fmt.Sprintf("guardrails: %s", err.Error()), buildToolError, contentType)
	}
	return args, nil
}

func applyModifiedArguments(mcpReq *MCPRequest, modified string, requestID any, buildToolError guardrailsToolErrorBuilder, contentType string) *Decision {
	if mcpReq == nil || modified == "" {
		return nil
	}
	params, err := replaceMapJSON(mcpReq.Params, "arguments", modified)
	if err != nil {
		return jsonRPCErrorDecision(400, requestID, fmt.Sprintf("guardrails: %s", err.Error()), buildToolError, contentType)
	}
	mcpReq.Params = params
	return nil
}

func applyModifiedElicitation(mcpReq *MCPRequest, modified string, requestID any, buildToolError guardrailsToolErrorBuilder, contentType string) *Decision {
	if mcpReq == nil || modified == "" {
		return nil
	}
	result, err := replaceElicitationContent(mcpReq.Result, modified)
	if err != nil {
		return jsonRPCErrorDecision(400, requestID, fmt.Sprintf("guardrails: %s", err.Error()), buildToolError, contentType)
	}
	mcpReq.Result = result
	return nil
}

func isElicitationAccept(req *MCPRequest) bool {
	return req != nil && req.IsElicitationResponse() && elicitationAction(req.Result) == elicitationActionAccept
}

// toolCallArguments JSON-encodes params["arguments"]. Missing or nil becomes {}.
func toolCallArguments(params map[string]any) (json.RawMessage, error) {
	if params == nil {
		return json.RawMessage(`{}`), nil
	}
	args, ok := params["arguments"]
	if !ok || args == nil {
		return json.RawMessage(`{}`), nil
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal tool arguments: %w", err)
	}
	return raw, nil
}

// elicitationArguments is result minus "action", JSON-encoded.
func elicitationArguments(result map[string]any) (json.RawMessage, error) {
	if result == nil {
		return json.RawMessage(`{}`), nil
	}
	rest := make(map[string]any, len(result))
	for k, v := range result {
		if k == elicitationResultAction {
			continue
		}
		rest[k] = v
	}
	if len(rest) == 0 {
		return json.RawMessage(`{}`), nil
	}
	raw, err := json.Marshal(rest)
	if err != nil {
		return nil, fmt.Errorf("marshal elicitation result: %w", err)
	}
	return raw, nil
}

func elicitationAction(result map[string]any) string {
	if result == nil {
		return ""
	}
	action, ok := result[elicitationResultAction].(string)
	if !ok {
		return ""
	}
	return action
}

// replaceMapJSON unmarshals content and stores it at key. Returns a new-or-same map.
func replaceMapJSON(m map[string]any, key, content string) (map[string]any, error) {
	var v any
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return nil, fmt.Errorf("unmarshal modified %s: %w", key, err)
	}
	if m == nil {
		m = map[string]any{}
	}
	m[key] = v
	return m, nil
}

func replaceElicitationContent(result map[string]any, content string) (map[string]any, error) {
	var v any
	if err := json.Unmarshal([]byte(content), &v); err != nil {
		return nil, fmt.Errorf("unmarshal modified elicitation content: %w", err)
	}
	action := elicitationAction(result)
	rest, ok := v.(map[string]any)
	if !ok {
		rest = map[string]any{"content": v}
	}
	rest[elicitationResultAction] = action
	return rest, nil
}

// BuildJSONRPCError constructs a JSON-RPC error object for 2026-07-28
// guardrails rejections (not a tools/call isError result).
func BuildJSONRPCError(requestID any, message string) string {
	var b strings.Builder
	b.WriteString("{\"jsonrpc\":\"2.0\",\"id\":")
	idBytes, err := json.Marshal(requestID)
	if err != nil {
		b.WriteString("null")
	} else {
		b.Write(idBytes)
	}
	b.WriteString(",\"error\":{\"code\":-32000,\"message\":")
	b.WriteString(jsonQuote(message))
	b.WriteString("}}")
	return b.String()
}

// BuildSSEJSONRPCError constructs an SSE JSON-RPC error object for 2025-11-25
// guardrails rejections (not a tools/call isError result).
func BuildSSEJSONRPCError(requestID any, message string) string {
	return SseJSONRPC(requestID, func(b *strings.Builder) {
		b.WriteString(",\"error\":{\"code\":-32000,\"message\":")
		b.WriteString(jsonQuote(message))
		b.WriteString("}}")
	})
}

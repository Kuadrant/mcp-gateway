package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/guardrails/api"
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

// guardrailsCheck runs guardrails for one routing decision. Built from a
// locked config snapshot so checker, global config, and per-server IDs do
// not tear across reload.
type guardrailsCheck struct {
	checker     api.Checker
	global      *api.Config
	serverIDs   []string
	logger      *slog.Logger
	buildError  guardrailsToolErrorBuilder
	contentType string
}

// guardrailsOption configures optional fields on a new guardrailsCheck.
type guardrailsOption func(*guardrailsCheck)

// withSSEErrors configures 2025-11-25 SSE JSON-RPC error responses.
func withSSEErrors() guardrailsOption {
	return func(gc *guardrailsCheck) {
		gc.buildError = BuildSSEJSONRPCError
		gc.contentType = ""
	}
}

// newGuardrailsCheck builds a guardrails check for the given config IDs.
// cfg.GetGuardrails() is called once under a single read lock; no server
// scan is performed. Defaults are 2026-07-28 JSON errors; pass withSSEErrors
// for 2025-11-25.
func newGuardrailsCheck(cfg *config.MCPServersConfig, configIDs []string, logger *slog.Logger, opts ...guardrailsOption) *guardrailsCheck {
	var checker api.Checker
	var global *api.Config
	if cfg != nil {
		checker, global = cfg.GetGuardrails()
	}
	return newGuardrailsCheckFromCheckerAndIDs(checker, global, configIDs, logger, opts...)
}

// newGuardrailsCheckFromCheckerAndIDs builds a guardrails check from an
// already-loaded checker and global config. Used when the caller has obtained
// checker, global, and per-server config IDs under one atomic read (e.g.
// GuardrailsForServer), ensuring consistency across a concurrent reload.
func newGuardrailsCheckFromCheckerAndIDs(checker api.Checker, global *api.Config, configIDs []string, logger *slog.Logger, opts ...guardrailsOption) *guardrailsCheck {
	gc := &guardrailsCheck{
		checker:     checker,
		global:      global,
		serverIDs:   configIDs,
		logger:      logger,
		buildError:  BuildJSONRPCError,
		contentType: "application/json",
	}
	for _, o := range opts {
		o(gc)
	}
	return gc
}

// checkToolCall extracts tool arguments, runs the guardrails check, and
// applies any modification onto mcpReq in place. modified is true when
// arguments were rewritten (callers that buffer the body must re-marshal).
func (g *guardrailsCheck) checkToolCall(ctx context.Context, mcpReq *MCPRequest, toolName string) (modified bool, blocked *Decision) {
	var requestID any
	if mcpReq != nil {
		requestID = mcpReq.ID
	}
	args, blocked := g.toolCallArguments(ctx, mcpReq, requestID)
	if blocked != nil {
		return false, blocked
	}
	content, blocked := g.request(ctx, toolName, args, requestID)
	if blocked != nil {
		return false, blocked
	}
	if content == "" {
		return false, nil
	}
	if blocked := g.applyModifiedArguments(ctx, mcpReq, content, requestID); blocked != nil {
		return false, blocked
	}
	return true, nil
}

// checkToolCallResponse runs guardrails against the text content of a
// tools/call response. Returns a replacement body for the client when the
// response must be blocked or has been modified, or nil to pass through the
// original. buildToolError formats an isError tool result; buildToolResult
// formats a successful tool result (used when StatusModified redacts content).
func (g *guardrailsCheck) checkToolCallResponse(ctx context.Context, toolName string, textContent []byte, requestID any, buildToolError guardrailsToolErrorBuilder, buildToolResult guardrailsToolErrorBuilder) []byte {
	if len(textContent) == 0 {
		return nil
	}
	modified, isModified, blockMsg := g.responseCheck(ctx, toolName, textContent)
	switch {
	case blockMsg != "":
		return []byte(buildToolError(requestID, blockMsg))
	case isModified:
		// guardrails redacted the content: return it as a successful result,
		// not as an error — StatusModified means "safe version", not "failure".
		// content may be empty when the checker redacted everything.
		return []byte(buildToolResult(requestID, modified))
	default:
		return nil
	}
}

// responseCheck runs a guardrails check on tools/call response text.
// Returns (modifiedContent, isModified, blockMessage).
// isModified distinguishes StatusModified (even with empty content) from
// StatusAllowed — a checker may return StatusModified with empty content
// to signal that all text was redacted; treating that as pass-through would
// forward the original response, violating the guardrails decision.
// Non-empty blockMessage is the client-visible reason for a block or failure.
func (g *guardrailsCheck) responseCheck(ctx context.Context, toolName string, content []byte) (modified string, isModified bool, blockMessage string) {
	var globalConfigIDs []string
	if g.global != nil {
		globalConfigIDs = g.global.ConfigIDs
	}
	if len(globalConfigIDs) == 0 && len(g.serverIDs) == 0 {
		return "", false, ""
	}
	if g.checker == nil {
		g.logError(ctx, "guardrails checker unavailable for response check", toolName, fmt.Errorf("checker is nil"))
		return "", false, guardrailsUnavailableMessage
	}

	decision, checkErr := g.checker.CheckResponse(ctx, toolName, content, g.serverIDs)
	if checkErr != nil {
		g.logError(ctx, "guardrails response translation failed", toolName, checkErr)
		return "", false, guardrailsCheckFailedMessage
	}
	if decision == nil {
		g.logError(ctx, "guardrails returned nil decision for response", toolName, fmt.Errorf("nil decision"))
		return "", false, guardrailsCheckFailedMessage
	}

	switch decision.Status {
	case api.StatusBlocked:
		if decision.Err != nil {
			g.logError(ctx, "guardrails response check unavailable, failing closed", toolName, decision.Err)
			return "", false, guardrailsUnavailableMessage
		}
		if g.logger != nil {
			g.logger.InfoContext(ctx, "guardrails blocked tool response", "tool", toolName, "reason", decision.Reason)
		}
		return "", false, guardrailsBlockedMessage
	case api.StatusAllowed:
		if decision.Err != nil && g.logger != nil {
			g.logger.ErrorContext(ctx, "guardrails response check failed open", "tool", toolName, "error", decision.Err)
		}
		return "", false, ""
	case api.StatusModified:
		return decision.Content, true, ""
	default:
		g.logError(ctx, "guardrails returned unrecognized status for response", toolName, fmt.Errorf("status %q", decision.Status))
		return "", false, guardrailsCheckFailedMessage
	}
}

// CheckToolResponseGuardrails is the entry point for response-phase
// guardrails used by the ext_proc adapter. configIDs are the per-server IDs
// from MCPRequest.GuardrailsConfigIDs while global IDs are loaded from cfg.
// Returns a replacement body when the response must be blocked or modified,
// or nil to pass through the original body.
// buildToolError formats an isError tool result (blocked) while buildToolResult
// formats a successful tool result (StatusModified redacted content).
// withSSEErrors is passed so that g.buildError matches the 2025-11-25 call site;
// checkToolCallResponse does not call g.buildError directly, but a future caller
// of g.errorDecision inside responseCheck would use the wrong format without it.
func CheckToolResponseGuardrails(ctx context.Context, cfg *config.MCPServersConfig, toolName string, configIDs []string, textContent []byte, requestID any, logger *slog.Logger, buildToolError func(any, string) string, buildToolResult func(any, string) string) []byte {
	gc := newGuardrailsCheck(cfg, configIDs, logger, withSSEErrors())
	return gc.checkToolCallResponse(ctx, toolName, textContent, requestID, buildToolError, buildToolResult)
}

// checkElicitationAccept runs the guardrails check for an elicitation accept
// and applies any modification in place. Decline/cancel and non-elicitation
// requests are skipped. requestID is the client-facing id for error bodies.
func (g *guardrailsCheck) checkElicitationAccept(ctx context.Context, mcpReq *MCPRequest, requestID any) *Decision {
	if !isElicitationAccept(mcpReq) {
		return nil
	}
	args, err := elicitationArguments(mcpReq.Result)
	if err != nil {
		g.logError(ctx, "guardrails elicitation arguments failed", elicitationActionAccept, err)
		return g.errorDecision(400, requestID, guardrailsCheckFailedMessage)
	}
	content, blocked := g.request(ctx, elicitationActionAccept, args, requestID)
	if blocked != nil {
		return blocked
	}
	return g.applyModifiedElicitation(ctx, mcpReq, content, requestID)
}

// request runs the guardrails check for name/arguments. blocked is non-nil
// when the request must not proceed. modified content is returned when the
// verdict is StatusModified. Empty merged config IDs skip the check.
// Non-empty IDs with no Checker fail closed (503).
func (g *guardrailsCheck) request(ctx context.Context, name string, arguments json.RawMessage, requestID any) (modified string, blocked *Decision) {
	var globalConfigIDs []string
	if g.global != nil {
		globalConfigIDs = g.global.ConfigIDs
	}
	if len(globalConfigIDs) == 0 && len(g.serverIDs) == 0 {
		return "", nil
	}

	if g.checker == nil {
		return "", g.errorDecision(503, requestID, guardrailsUnavailableMessage)
	}

	decision, checkErr := g.checker.CheckRequest(ctx, name, arguments, g.serverIDs)
	if checkErr != nil {
		// translation failure only; always a hard deny, failMode does not
		// apply. checkErr can carry internal transport/provider detail, so
		// it's logged rather than returned to the client.
		g.logError(ctx, "guardrails request translation failed", name, checkErr)
		return "", g.errorDecision(400, requestID, guardrailsCheckFailedMessage)
	}

	if decision == nil {
		g.logError(ctx, "guardrails returned nil decision", name, fmt.Errorf("nil decision"))
		return "", g.errorDecision(400, requestID, guardrailsCheckFailedMessage)
	}

	switch decision.Status {
	case api.StatusBlocked:
		if decision.Err != nil {
			g.logError(ctx, "guardrails check unavailable, failing closed", name, decision.Err)
			return "", g.errorDecision(503, requestID, guardrailsUnavailableMessage)
		}
		if g.logger != nil {
			g.logger.InfoContext(ctx, "guardrails blocked request", "tool", name, "reason", decision.Reason)
		}
		return "", g.errorDecision(403, requestID, guardrailsBlockedMessage)
	case api.StatusAllowed:
		if decision.Err != nil && g.logger != nil {
			g.logger.ErrorContext(ctx, "guardrails check failed open", "tool", name, "error", decision.Err)
		}
		return "", nil
	case api.StatusModified:
		return decision.Content, nil
	default:
		g.logError(ctx, "guardrails returned unrecognized status", name, fmt.Errorf("status %q", decision.Status))
		return "", g.errorDecision(400, requestID, guardrailsCheckFailedMessage)
	}
}

func (g *guardrailsCheck) errorDecision(status int, requestID any, message string) *Decision {
	return jsonRPCErrorDecision(status, requestID, message, g.buildError, g.contentType)
}

func (g *guardrailsCheck) logError(ctx context.Context, msg, toolName string, err error) {
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

func (g *guardrailsCheck) toolCallArguments(ctx context.Context, mcpReq *MCPRequest, requestID any) (json.RawMessage, *Decision) {
	if mcpReq == nil {
		return nil, nil
	}
	args, err := toolCallArguments(mcpReq.Params)
	if err != nil {
		g.logError(ctx, "guardrails tool arguments failed", "", err)
		return nil, g.errorDecision(400, requestID, guardrailsCheckFailedMessage)
	}
	return args, nil
}

func (g *guardrailsCheck) applyModifiedArguments(ctx context.Context, mcpReq *MCPRequest, modified string, requestID any) *Decision {
	if mcpReq == nil || modified == "" {
		return nil
	}
	params, err := replaceMapJSON(mcpReq.Params, "arguments", modified)
	if err != nil {
		g.logError(ctx, "guardrails apply modified arguments failed", "", err)
		return g.errorDecision(400, requestID, guardrailsCheckFailedMessage)
	}
	mcpReq.Params = params
	return nil
}

func (g *guardrailsCheck) applyModifiedElicitation(ctx context.Context, mcpReq *MCPRequest, modified string, requestID any) *Decision {
	if mcpReq == nil || modified == "" {
		return nil
	}
	result, err := replaceElicitationContent(mcpReq.Result, modified)
	if err != nil {
		g.logError(ctx, "guardrails apply modified elicitation failed", "", err)
		return g.errorDecision(400, requestID, guardrailsCheckFailedMessage)
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

package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Kuadrant/mcp-gateway/internal/config"
	"github.com/Kuadrant/mcp-gateway/internal/guardrails"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// GuardrailsCheckerFunc returns the current guardrails Checker, or nil when
// guardrails is not configured. Backed by an atomic.Pointer owned by the
// ext_proc adapter and shared by both protocol routers, so a config reload
// is visible without either router needing to import the adapter package.
type GuardrailsCheckerFunc func() guardrails.Checker

// guardrailsToolErrorBuilder builds the transport-specific JSON-RPC error
// object for a blocked request: BuildSSEJSONRPCError for 2025-11-25,
// BuildJSONRPCError for 2026-07-28.
type guardrailsToolErrorBuilder func(requestID any, message string) string

const guardrailsUnavailableMessage = "guardrails check unavailable"

// checkGuardrailsRequest runs the guardrails check for a tools/call or
// elicitation accept. blocked is non-nil when the request must not proceed.
// modified is the guardrails-rewritten arguments/content when Status is
// StatusModified; callers must apply it before forwarding. Empty IDs skip
// the check and set guardrails.enabled=false. Non-empty IDs with no Checker
// fail closed (503).
func checkGuardrailsRequest(
	ctx context.Context,
	span trace.Span,
	logger *slog.Logger,
	checkerFn GuardrailsCheckerFunc,
	globalGuardrails *config.GuardrailsConfig,
	serverConfigIDs []string,
	name string,
	arguments json.RawMessage,
	requestID any,
	buildToolError guardrailsToolErrorBuilder,
	contentType string,
) (modified string, blocked *Decision) {
	var globalConfigIDs []string
	failMode := guardrails.FailModeDeny
	if globalGuardrails != nil {
		globalConfigIDs = globalGuardrails.ConfigIDs
		if globalGuardrails.FailMode != "" {
			failMode = globalGuardrails.FailMode
		}
	}
	if len(globalConfigIDs) == 0 && len(serverConfigIDs) == 0 {
		span.SetAttributes(attribute.Bool("guardrails.enabled", false))
		return "", nil
	}

	span.SetAttributes(
		attribute.Bool("guardrails.enabled", true),
		attribute.StringSlice("guardrails.config_ids", append(append([]string{}, globalConfigIDs...), serverConfigIDs...)),
		attribute.String("guardrails.fail_mode", failMode),
	)

	var checker guardrails.Checker
	if checkerFn != nil {
		checker = checkerFn()
	}
	if checker == nil {
		span.SetAttributes(attribute.String("guardrails.status", "error"))
		return "", jsonRPCErrorDecision(503, requestID, guardrailsUnavailableMessage, buildToolError, contentType)
	}

	start := time.Now()
	decision, checkErr := checker.CheckRequest(ctx, name, arguments, serverConfigIDs)
	span.SetAttributes(attribute.Int64("guardrails.latency_ms", time.Since(start).Milliseconds()))
	if checkErr != nil {
		// translation failure only; always a hard deny, failMode does not apply
		span.SetAttributes(attribute.String("guardrails.status", "error"))
		if logger != nil {
			logger.DebugContext(ctx, "guardrails check", "status", "error", "error", checkErr)
		}
		return "", jsonRPCErrorDecision(400, requestID, fmt.Sprintf("guardrails: %s", checkErr.Error()), buildToolError, contentType)
	}

	if logger != nil {
		logger.DebugContext(ctx, "guardrails check", "status", decision.Status, "reason", decision.Reason)
	}

	switch decision.Status {
	case guardrails.StatusBlocked:
		if decision.Err != nil {
			span.SetAttributes(attribute.String("guardrails.status", "error"))
			return "", jsonRPCErrorDecision(503, requestID, guardrailsUnavailableMessage, buildToolError, contentType)
		}
		span.SetAttributes(attribute.String("guardrails.status", "blocked"))
		return "", jsonRPCErrorDecision(403, requestID, guardrailsBlockedMessage(decision.Reason), buildToolError, contentType)
	case guardrails.StatusAllowed:
		span.SetAttributes(attribute.String("guardrails.status", "success"))
		return "", nil
	case guardrails.StatusModified:
		span.SetAttributes(attribute.String("guardrails.status", "success"))
		return decision.Content, nil
	default:
		span.SetAttributes(attribute.String("guardrails.status", "error"))
		return "", jsonRPCErrorDecision(400, requestID, fmt.Sprintf("guardrails: unrecognized status %q", decision.Status), buildToolError, contentType)
	}
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

func guardrailsBlockedMessage(reason string) string {
	if reason == "" {
		return "blocked by guardrails"
	}
	return fmt.Sprintf("blocked by guardrails: %s", reason)
}

func guardrailsArguments(mcpReq *MCPRequest, requestID any, buildToolError guardrailsToolErrorBuilder, contentType string) (json.RawMessage, *Decision) {
	if mcpReq == nil {
		return nil, nil
	}
	args, err := mcpReq.Arguments()
	if err != nil {
		return nil, jsonRPCErrorDecision(400, requestID, fmt.Sprintf("guardrails: %s", err.Error()), buildToolError, contentType)
	}
	return args, nil
}

func applyModifiedArguments(mcpReq *MCPRequest, modified string, requestID any, buildToolError guardrailsToolErrorBuilder, contentType string) *Decision {
	if mcpReq == nil || modified == "" {
		return nil
	}
	if err := mcpReq.SetArgumentsJSON(modified); err != nil {
		return jsonRPCErrorDecision(400, requestID, fmt.Sprintf("guardrails: %s", err.Error()), buildToolError, contentType)
	}
	return nil
}

func applyModifiedElicitation(mcpReq *MCPRequest, modified string, requestID any, buildToolError guardrailsToolErrorBuilder, contentType string) *Decision {
	if mcpReq == nil || modified == "" {
		return nil
	}
	if err := mcpReq.SetElicitationContentJSON(modified); err != nil {
		return jsonRPCErrorDecision(400, requestID, fmt.Sprintf("guardrails: %s", err.Error()), buildToolError, contentType)
	}
	return nil
}

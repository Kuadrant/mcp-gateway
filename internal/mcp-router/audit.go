package mcprouter

import (
	"encoding/json"
)

const (
	maxParamBytes = 1024
	maxIDBytes    = 256
)

// setAuditHeaders adds audit-related headers (user ID, agent ID, tool params)
// to the request when auditing is enabled. No-op when Audit config is nil.
func (s *ExtProcServer) setAuditHeaders(mcpReq *MCPRequest, headers *HeadersBuilder) {
	if s.Audit == nil {
		return
	}
	baggage := mcpReq.GetSingleHeaderValue(baggageHeader)
	baggageUserID, agentID := parseBaggage(baggage)

	identityHeaders := defaultIdentityHeaders
	if len(s.Audit.IdentityHeaders) > 0 {
		identityHeaders = s.Audit.IdentityHeaders
	}

	userID := truncateID(stripControlChars(resolveUserID(baggageUserID, mcpReq.GetSingleHeaderValue, identityHeaders)))
	if userID == "" {
		userID = "-"
	}
	agentID = truncateID(agentID)
	if agentID == "" {
		agentID = "-"
	}
	headers.WithMCPUserID(userID)
	headers.WithMCPAgentID(agentID)

	logParams := s.Audit.ParameterLogging == "Enabled"
	params := extractToolParams(logParams, mcpReq.Params)
	if params == "" {
		params = "-"
	}
	headers.WithMCPToolParams(params)
}

func truncateID(s string) string {
	if len(s) > maxIDBytes {
		return s[:maxIDBytes]
	}
	return s
}

// extractToolParams serializes params.arguments as JSON when enabled.
// Returns empty string when disabled, when params is nil, or when
// arguments is absent. Truncates to maxParamBytes.
// hot path (called per tools/call), but gated by ParameterLogging config
func extractToolParams(enabled bool, params map[string]any) string {
	if !enabled || params == nil {
		return ""
	}
	args, ok := params["arguments"]
	if !ok {
		return ""
	}
	b, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	if len(b) > maxParamBytes {
		// replacing instead of slicing: truncating raw bytes would produce invalid JSON and may split a multi-byte UTF-8 character
		return "[truncated]"
	}
	return string(b)
}

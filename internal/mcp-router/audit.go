package mcprouter

import (
	"encoding/json"

	"google.golang.org/protobuf/types/known/structpb"
)

const (
	maxParamBytes     = 1024
	maxIDBytes        = 256
	auditMetadataNS   = "mcp.audit"
	metadataUserID    = "user_id"
	metadataAgentID   = "agent_id"
	metadataToolParam = "tool_params"
)

// buildAuditMetadata builds dynamic metadata for audit logging.
// Returns nil when Audit config is nil (no-op for non-audit deployments).
func (s *ExtProcServer) buildAuditMetadata(mcpReq *MCPRequest) *structpb.Struct {
	if s.Audit == nil {
		return nil
	}
	baggage := mcpReq.GetSingleHeaderValue(baggageHeader)
	baggageUserID, agentID := parseBaggage(baggage)

	userID := truncateID(stripControlChars(resolveUserID(baggageUserID, mcpReq.GetSingleHeaderValue, s.Audit.IdentityHeaders)))
	if userID == "" {
		userID = "-"
	}
	agentID = truncateID(agentID)
	if agentID == "" {
		agentID = "-"
	}

	logParams := s.Audit.ParameterLogging == "Enabled"
	params := extractToolParams(logParams, mcpReq.Params)
	if params == "" {
		params = "-"
	}

	md, _ := structpb.NewStruct(map[string]any{
		auditMetadataNS: map[string]any{
			metadataUserID:    userID,
			metadataAgentID:   agentID,
			metadataToolParam: params,
		},
	})
	return md
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
		return "[truncated]"
	}
	return string(b)
}

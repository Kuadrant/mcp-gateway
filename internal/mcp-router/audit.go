package mcprouter

import (
	"encoding/json"
	"os"
)

// ExtractToolParams extracts and serializes the tool call arguments from the MCPRequest,
// returning a JSON string truncated to 1KB if parameter logging is enabled.
func ExtractToolParams(mcpReq *MCPRequest) string {
	if mcpReq == nil || !mcpReq.isToolCall() || mcpReq.Params == nil {
		return ""
	}
	// Check if MCP_AUDIT_LOG_PARAMS env var is set to "true"
	if os.Getenv("MCP_AUDIT_LOG_PARAMS") != "true" {
		return ""
	}

	args, ok := mcpReq.Params["arguments"]
	if !ok || args == nil {
		return ""
	}

	bytes, err := json.Marshal(args)
	if err != nil {
		return ""
	}

	str := string(bytes)
	if len(str) > 1024 {
		str = str[:1024]
	}
	return str
}

package guardrails

// Config holds the resolved guardrails server config parsed from
// the guardrails Secret referenced by the MCPGatewayExtension.
type Config struct {
	URL       string   `json:"url"                 yaml:"url"`
	ConfigIDs []string `json:"configIDs,omitempty" yaml:"configIDs,omitempty"`
	Model     string   `json:"model"               yaml:"model"`
	FailMode  string   `json:"failMode,omitempty"  yaml:"failMode,omitempty"` // "deny" | "allow"
}

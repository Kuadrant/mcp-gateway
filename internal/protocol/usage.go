package protocol

// UsagePath is served on the broker metrics port and read by the controller,
// which records the versions in MCPServerRegistration status.
const UsagePath = "/protocol-usage"

// DefaultClientVersion is assumed for clients that declare no version,
// matching the SDK and broker default.
const DefaultClientVersion = "2025-03-26"

// ClientVersions are the client protocol versions the gateway recognises,
// newest first. Anything else is not recorded.
var ClientVersions = []string{Version2026, Version2025, "2025-06-18", DefaultClientVersion, "2024-11-05"}

// Usage holds protocol versions per upstream server, keyed by
// MCPServerRegistration namespace/name.
type Usage struct {
	// Used lists the versions clients used with each server since the broker
	// process started
	Used map[string][]string `json:"used"`
	// Discovered lists the versions each connected server supports
	Discovered map[string][]string `json:"discovered"`
}

package protocol

// UsagePath is served on the broker metrics port and read by the controller,
// which records the versions in MCPServerRegistration status.usedProtocolVersions.
const UsagePath = "/protocol-usage"

// DefaultClientVersion is assumed for clients that declare no version,
// matching the SDK and broker default.
const DefaultClientVersion = "2025-03-26"

// ClientVersions are the client protocol versions the gateway recognises,
// newest first. Anything else is not recorded.
var ClientVersions = []string{Version2026, Version2025, "2025-06-18", DefaultClientVersion, "2024-11-05"}

// Usage lists the protocol versions clients used with each upstream server
// since the broker process started, keyed by MCPServerRegistration namespace/name.
type Usage struct {
	Servers map[string][]string `json:"servers"`
}

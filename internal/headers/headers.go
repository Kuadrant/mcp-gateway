// Package headers defines shared HTTP header constants used across router and broker.
package headers

import (
	"slices"
	"strings"
)

// Elicitation headers used by both the ext-proc router and the broker HTTP handler.
const (
	ElicitationRequestID = "x-mcp-request-id"
	ElicitationID        = "x-mcp-elicitation-id"

	// VerifiedSubHeader carries the JWT sub claim after the router has validated
	// the Authorization token via AuthPolicy. The broker reads this header to
	// bind token submissions to a verified identity without re-parsing the JWT.
	// Stripped from any client-supplied value by the router.
	VerifiedSubHeader = "x-mcp-verified-sub"
)

// BrowserHopHeaders are removed before requests leave the gateway.
var BrowserHopHeaders = []string{"origin", "referer", "cookie", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest", "sec-fetch-user"}

// IsBrowserHop reports whether name is scoped to the browser-to-gateway hop.
func IsBrowserHop(name string) bool {
	return slices.Contains(BrowserHopHeaders, strings.ToLower(name))
}

// A2A protocol-metadata headers, set by the ext-proc router from the request
// path and body when A2A passthrough is enabled, so Istio Telemetry and
// AuthPolicy can key on the agent and method. Router-derived and stripped from
// any client-supplied value, so a client cannot forge what policy keys on.
const (
	A2AAgentHeader  = "x-a2a-agent"  // agent identity, from the /a2a/{agent} path segment
	A2AMethodHeader = "x-a2a-method" // A2A JSON-RPC method (normalized to a bounded set)
)

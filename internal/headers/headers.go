// Package headers defines shared HTTP header constants used across router and broker.
package headers

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

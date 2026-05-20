// Package headers defines shared HTTP header constants used across router and broker.
package headers

// Elicitation headers used by both the ext-proc router and the broker HTTP handler.
const (
	ElicitationRequestID = "x-mcp-request-id"
	ElicitationID        = "x-mcp-elicitation-id"
)

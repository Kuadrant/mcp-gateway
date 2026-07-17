package routing

import "context"

// Router defines the routing contract independent of any transport adapter.
// Implementations handle protocol-specific routing logic.
type Router interface {
	RouteRequest(ctx context.Context, req *Request) *Decision
}

// Request is a transport-agnostic representation of an incoming request.
type Request struct {
	MCPMethod       string // Mcp-Method header (2026-07-28) or parsed from body (2025-11-25)
	MCPName         string // Mcp-Name header (2026-07-28) or parsed from body (2025-11-25)
	ProtocolVersion string // MCP-Protocol-Version header
	Authority       string // :authority header
	SessionID       string // mcp-session-id header (2025-11-25 only)
	Path            string // :path header
	RequestID       string // x-request-id

	Body   []byte      // raw body bytes (nil if body phase not entered)
	Parsed *MCPRequest // parsed JSON-RPC (nil if body not parsed)

	RawHeaders map[string]string
}

// Decision is the output of a routing decision.
type Decision struct {
	Authority    string            // :authority header value (backend HTTPRoute hostname)
	Path         string            // :path override (backend path)
	SetHeaders   map[string]string // headers to set on the request
	UnsetHeaders []string          // headers to remove
	BodyMutation []byte            // nil if no body change needed
	Error        *Error            // non-nil if the request should be rejected
	BrokerPass   bool              // true if request should pass through to broker unchanged
}

// Error represents a rejection with optional JSON-RPC error body.
type Error struct {
	StatusCode int
	Message    string
	JSONRPCErr string // optional JSON-RPC error body for SSE responses
}

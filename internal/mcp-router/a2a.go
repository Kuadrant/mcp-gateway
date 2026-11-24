package mcprouter

// A2A passthrough (phase 1): when --enable-a2a is set, the router lifts A2A
// protocol metadata off /a2a traffic into headers so Istio Telemetry and Kuadrant
// AuthPolicy can key on the agent and method. It does not route, rewrite, or
// inspect anything else — the user's own HTTPRoute carries the request to the
// agent. See docs/design/a2a/a2a-design.md (Implementation Phases).

import (
	"encoding/json"
	"strings"

	"github.com/Kuadrant/mcp-gateway/internal/headers"
	"github.com/Kuadrant/mcp-gateway/internal/routing"
)

const a2aPathPrefix = "/a2a/"

// v1 A2A JSON-RPC method names the gateway recognizes. Anything else is
// normalized to a2aMethodOther so the x-a2a-method label stays a bounded set —
// a client-supplied method string must never become an unbounded metric label.
const (
	a2aMethodSendMessage     = "SendMessage"
	a2aMethodSendStreaming   = "SendStreamingMessage"
	a2aMethodGetTask         = "GetTask"
	a2aMethodCancelTask      = "CancelTask"
	a2aMethodSubscribeToTask = "SubscribeToTask"
	a2aMethodOther           = "other"
)

// JSON-RPC error codes for /a2a requests that fail closed rather than reaching the
// agent without the metadata this phase records: a2aErrParse for a body that is not
// valid JSON (or absent on a POST), a2aErrInvalidRequest for valid JSON that is not
// a usable JSON-RPC request (no method to label, or a method or id given more than
// once). The gateway does not otherwise validate the protocol version — the agent
// owns that.
const (
	a2aErrParse          = -32700
	a2aErrInvalidRequest = -32600
)

// a2aEnvelopeMembers are the envelope members parseA2AMethod reads.
var a2aEnvelopeMembers = [][]byte{[]byte("method"), []byte("id")}

// a2aInternalHeaders are the router-owned A2A headers. They are stripped from
// every request before the router sets its own, so a client cannot pre-seed the
// values AuthPolicy and Telemetry key on.
var a2aInternalHeaders = []string{headers.A2AAgentHeader, headers.A2AMethodHeader}

func isA2APath(path string) bool {
	return strings.HasPrefix(path, a2aPathPrefix)
}

// a2aAgentFromPath returns the agent identity — the first path segment after
// /a2a/ — used as the x-a2a-agent value. Empty when the path carries no segment
// (e.g. exactly "/a2a/"), in which case no agent header is set.
func a2aAgentFromPath(path string) string {
	rest := strings.TrimPrefix(path, a2aPathPrefix)
	if i := strings.IndexAny(rest, "/?"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// normalizeA2AMethod returns a known v1 method verbatim, or a2aMethodOther for
// anything unrecognized, keeping the x-a2a-method label bounded.
func normalizeA2AMethod(method string) string {
	switch method {
	case a2aMethodSendMessage, a2aMethodSendStreaming, a2aMethodGetTask, a2aMethodCancelTask, a2aMethodSubscribeToTask:
		return method
	}
	return a2aMethodOther
}

// parseA2AMethod reads only the JSON-RPC envelope — the method, plus the id for
// echoing in an error. params and everything else stay opaque, keeping the hot
// path proportional to the envelope, not the payload.
func parseA2AMethod(body []byte) (method string, id json.RawMessage, err error) {
	var env struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return "", nil, err
	}
	// a case-variant duplicate ("method" + "METHOD") would let the client pick
	// the label while the agent dispatches the other
	if err := routing.CheckDuplicateMembers(body, a2aEnvelopeMembers); err != nil {
		return "", env.ID, err
	}
	return env.Method, env.ID, nil
}

type a2aErrorObject struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type a2aErrorResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   a2aErrorObject  `json:"error"`
}

// a2aErrorBody builds a JSON-RPC error envelope, echoing the request id when known.
func a2aErrorBody(id json.RawMessage, code int, message string) string {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	body, err := json.Marshal(a2aErrorResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   a2aErrorObject{Code: code, Message: message},
	})
	if err != nil {
		return `{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"internal error"}}`
	}
	return string(body)
}

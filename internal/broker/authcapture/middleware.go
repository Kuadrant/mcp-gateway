// Package authcapture provides HTTP middleware to capture client auth headers for
// per-client backend SSE connections. Headers are injected into the request context
// on GET /mcp so the OnRegisterSession hook can read them and open per-client connections.
package authcapture

import (
	"context"
	"net/http"
	"strings"
)

type contextKey struct{}

// InjectHeaders wraps next and, on GET requests, stores a copy of the client's
// headers in the request context. All headers are captured except:
//   - HTTP/2 pseudo-headers (":authority", ":path", etc.) — not valid HTTP/1.1 headers
//   - "mcp-session-id" — this is a gateway-assigned session identifier scoped to the
//     client↔gateway connection; it must not be forwarded to backend servers because
//     the backend has its own independent session with the broker
//
// This matches the passthrough policy applied by the router for tool calls.
func InjectHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			headers := make(map[string]string, len(r.Header))
			for k, vs := range r.Header {
				k = strings.ToLower(k)
				if strings.HasPrefix(k, ":") || k == "mcp-session-id" {
					continue
				}
				if len(vs) > 0 {
					headers[k] = vs[0]
				}
			}
			r = r.WithContext(context.WithValue(r.Context(), contextKey{}, headers))
		}
		next.ServeHTTP(w, r)
	})
}

// ExtractHeaders reads the headers injected by InjectHeaders from the context.
// Returns false if no headers were captured (e.g. on non-GET requests).
func ExtractHeaders(ctx context.Context) (map[string]string, bool) {
	headers, ok := ctx.Value(contextKey{}).(map[string]string)
	return headers, ok
}

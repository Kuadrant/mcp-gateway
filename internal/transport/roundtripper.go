// Package transport provides shared HTTP round-trippers.
package transport

import (
	"net/http"
)

// HeaderRoundTripper injects custom headers into every outgoing HTTP request.
// It clones the request before modifying headers to avoid mutating the caller's original.
type HeaderRoundTripper struct {
	Base    http.RoundTripper
	Headers map[string]string
}

// RoundTrip implements http.RoundTripper.
func (h *HeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r2 := req.Clone(req.Context())
	for k, v := range h.Headers {
		r2.Header.Set(k, v)
	}
	return h.Base.RoundTrip(r2)
}

// DynamicHeaderRoundTripper injects headers resolved per request, for
// callers whose header set changes over a connection's lifetime (e.g. a
// pooled session that must always carry the caller's current credentials).
type DynamicHeaderRoundTripper struct {
	Base    http.RoundTripper
	Headers func() map[string]string
}

// RoundTrip implements http.RoundTripper.
func (d *DynamicHeaderRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r2 := req.Clone(req.Context())
	for k, v := range d.Headers() {
		r2.Header.Set(k, v)
	}
	return d.Base.RoundTrip(r2)
}

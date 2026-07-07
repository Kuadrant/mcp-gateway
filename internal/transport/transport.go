// Package transport provides shared HTTP round-trippers and wire helpers.
package transport

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
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

// PeekRequestBody returns the request body without consuming it, preferring
// GetBody and restoring req.Body otherwise. ok is false when the request has
// no body or reading it fails.
func PeekRequestBody(req *http.Request) (body []byte, ok bool) {
	switch {
	case req.GetBody != nil:
		rc, err := req.GetBody()
		if err != nil {
			return nil, false
		}
		body, err = io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return nil, false
		}
	case req.Body != nil:
		var err error
		body, err = io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, false
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	default:
		return nil, false
	}
	return body, true
}

// SSEEventData splits one SSE event into its data-line payloads, with the
// "data:" prefix and one optional leading space stripped. commentOnly
// reports whether every line was a comment or blank.
func SSEEventData(event []byte) (data [][]byte, commentOnly bool) {
	commentOnly = true
	for _, line := range bytes.Split(event, []byte("\n")) {
		if rest, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			data = append(data, bytes.TrimPrefix(rest, []byte(" ")))
			commentOnly = false
		} else if len(line) > 0 && line[0] != ':' {
			commentOnly = false
		}
	}
	return data, commentOnly
}

// DiscoverShortCircuit answers the SDK client's SEP-2575 server/discover
// probe in-process with the method-not-found error a mark3labs-era gateway
// produced, so the client falls straight back to the legacy initialize
// handshake with zero network round trips for the probe.
type DiscoverShortCircuit struct {
	Base http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (d *DiscoverShortCircuit) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost {
		if id, ok := discoverRequestID(req); ok {
			return synthesizeMethodNotFound(req, id), nil
		}
	}
	return d.Base.RoundTrip(req)
}

// discoverRequestID reports whether req is a server/discover call and
// returns its raw JSON-RPC id. the body is peeked without consuming it.
func discoverRequestID(req *http.Request) (json.RawMessage, bool) {
	body, ok := PeekRequestBody(req)
	if !ok {
		return nil, false
	}
	var env struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
	}
	if json.Unmarshal(body, &env) != nil || env.Method != "server/discover" {
		return nil, false
	}
	return env.ID, true
}

// synthesizeMethodNotFound builds the JSON response body a mark3labs
// server sent for server/discover.
func synthesizeMethodNotFound(req *http.Request, id json.RawMessage) *http.Response {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	var body bytes.Buffer
	body.WriteString(`{"jsonrpc":"2.0","id":`)
	body.Write(id)
	body.WriteString(`,"error":{"code":-32601,"message":"Method server/discover not found"}}` + "\n")
	header := make(http.Header, 2)
	header.Set("Content-Type", "application/json")
	header.Set("Content-Length", strconv.Itoa(body.Len()))
	return &http.Response{
		Status:        http.StatusText(http.StatusOK),
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        header,
		Body:          io.NopCloser(&body),
		ContentLength: int64(body.Len()),
		Request:       req,
	}
}

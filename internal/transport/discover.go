package transport

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
)

// DiscoverShortCircuit answers the SDK client's SEP-2575 server/discover
// probe in-process with the method-not-found error a mark3labs-era gateway
// produced, so the client falls straight back to the legacy initialize
// handshake. mark3labs clients never sent the probe, so backends behind the
// gateway must not see it either: with this in place a lazy backend init
// costs exactly the requests it did before (initialize +
// notifications/initialized), with zero network round trips for the probe.
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

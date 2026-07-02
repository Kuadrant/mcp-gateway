package transport

import (
	"bytes"
	"io"
	"net/http"
)

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

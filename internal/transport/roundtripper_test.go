package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureTransport records the request it receives.
type captureTransport struct {
	got *http.Request
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.got = req
	rec := httptest.NewRecorder()
	rec.WriteHeader(http.StatusOK)
	return rec.Result(), nil
}

func newRequest(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://upstream.local/mcp", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

func TestHeaderRoundTripper_InjectsHeaders(t *testing.T) {
	base := &captureTransport{}
	rt := &HeaderRoundTripper{
		Base:    base,
		Headers: map[string]string{"Authorization": "Bearer abc", "X-Custom": "v"},
	}

	req := newRequest(t)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := base.got.Header.Get("Authorization"); got != "Bearer abc" {
		t.Errorf("expected injected Authorization, got %q", got)
	}
	if got := base.got.Header.Get("X-Custom"); got != "v" {
		t.Errorf("expected injected X-Custom, got %q", got)
	}
}

func TestHeaderRoundTripper_OverridesExistingHeader(t *testing.T) {
	base := &captureTransport{}
	rt := &HeaderRoundTripper{
		Base:    base,
		Headers: map[string]string{"Authorization": "Bearer injected"},
	}

	req := newRequest(t)
	req.Header.Set("Authorization", "Bearer original")
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := base.got.Header.Get("Authorization"); got != "Bearer injected" {
		t.Errorf("expected injected value to win, got %q", got)
	}
}

// the round tripper must clone: http.RoundTripper contract forbids mutating
// the caller's request, and callers may retry with the original.
func TestHeaderRoundTripper_DoesNotMutateOriginalRequest(t *testing.T) {
	base := &captureTransport{}
	rt := &HeaderRoundTripper{
		Base:    base,
		Headers: map[string]string{"Authorization": "Bearer abc"},
	}

	req := newRequest(t)
	req.Header.Set("Accept", "application/json")
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("original request must not gain injected headers, got %q", got)
	}
	if base.got == req {
		t.Error("base transport must receive a clone, not the original request")
	}
	if got := base.got.Header.Get("Accept"); got != "application/json" {
		t.Errorf("clone must keep the original headers, got %q", got)
	}
}

func TestHeaderRoundTripper_NilHeaders(t *testing.T) {
	base := &captureTransport{}
	rt := &HeaderRoundTripper{Base: base}

	req := newRequest(t)
	req.Header.Set("Accept", "application/json")
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip with nil headers: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := base.got.Header.Get("Accept"); got != "application/json" {
		t.Errorf("expected original headers preserved, got %q", got)
	}
}
